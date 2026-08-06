package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRefuseInternalAddressBlocksTheRangesThatMatter covers the dial
// hook directly, since a fetch test cannot reach a public address.
func TestRefuseInternalAddressBlocksTheRangesThatMatter(t *testing.T) {
	blocked := []struct {
		name string
		addr string
	}{
		{"ipv4 loopback", "127.0.0.1:80"},
		{"ipv4 loopback range", "127.9.9.9:8443"},
		{"ipv6 loopback", "[::1]:80"},
		{"rfc1918 ten", "10.10.10.1:8443"},
		{"rfc1918 172", "172.16.4.9:80"},
		{"rfc1918 192", "192.168.1.1:80"},
		{"link local", "169.254.1.1:80"},
		{"cloud metadata", "169.254.169.254:80"},
		{"ipv6 link local", "[fe80::1]:80"},
		{"ipv6 unique local", "[fd00::1]:80"},
		{"unspecified", "0.0.0.0:80"},
		{"ipv4 mapped loopback", "[::ffff:127.0.0.1]:80"},
		{"ipv4 mapped rfc1918", "[::ffff:10.0.0.1]:80"},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			if err := refuseInternalAddress("tcp", tc.addr, nil); err == nil {
				t.Errorf("dial to %s was permitted", tc.addr)
			}
		})
	}

	allowed := []string{"1.1.1.1:443", "93.184.216.34:80", "[2606:4700::1111]:443"}
	for _, addr := range allowed {
		if err := refuseInternalAddress("tcp", addr, nil); err != nil {
			t.Errorf("dial to public address %s was refused: %v", addr, err)
		}
	}
}

// TestRefuseInternalAddressRejectsNonTCP keeps the hook closed for a
// network the HTTP transport should never hand it.
func TestRefuseInternalAddressRejectsNonTCP(t *testing.T) {
	if err := refuseInternalAddress("unix", "/run/lankeeper/agent.sock", nil); err == nil {
		t.Error("a unix socket dial was permitted")
	}
	if err := refuseInternalAddress("tcp", "not-an-address", nil); err == nil {
		t.Error("a malformed address was permitted")
	}
}

func TestValidateOutboundURL(t *testing.T) {
	valid := []string{"http://example.com/list.txt", "https://example.com:8443/a", " https://example.com/x "}
	for _, u := range valid {
		if err := validateOutboundURL(u); err != nil {
			t.Errorf("%q was rejected: %v", u, err)
		}
	}

	invalid := []string{
		"file:///etc/lankeeper/router.yaml",
		"gopher://example.com/",
		"ftp://example.com/x",
		"/etc/passwd",
		"http://",
		"",
	}
	for _, u := range invalid {
		if err := validateOutboundURL(u); err == nil {
			t.Errorf("%q was accepted", u)
		}
	}
}

// TestM3UFetchRefusesLoopbackServer is the end-to-end regression test
// for the web-reachable path. httptest always listens on loopback,
// which is exactly one of the destinations the discover-groups form
// could previously be pointed at.
func TestM3UFetchRefusesLoopbackServer(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 group-title=\"secret\",internal\nhttp://x/y\n"))
	}))
	defer srv.Close()

	_, err := downloadAndParseM3U(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("the fetch against a loopback server succeeded")
	}
	if !strings.Contains(err.Error(), "internal address") {
		t.Errorf("expected the guard to reject the dial, got: %v", err)
	}
	if hits != 0 {
		t.Errorf("the server was reached %d times; the request must not leave the process", hits)
	}
}

// TestBlocklistFetchRefusesLoopbackServer covers the second fetch path
// against the same guard.
func TestBlocklistFetchRefusesLoopbackServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0.0.0.0 ads.example\n"))
	}))
	defer srv.Close()

	if _, err := downloadBlocklist(context.Background(), srv.URL); err == nil {
		t.Error("the blocklist fetch against a loopback server succeeded")
	}
}

// TestHTTPProbeRefusesLoopbackServer covers the third path. httpProbe
// reports a boolean, so a refused dial has to read as "down".
func TestHTTPProbeRefusesLoopbackServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if httpProbe(context.Background(), srv.URL, http.StatusNoContent) {
		t.Error("the probe against a loopback server reported success")
	}
}

// TestFetchRefusesNonHTTPScheme keeps file:// out of the fetch paths
// before the transport ever sees it.
func TestFetchRefusesNonHTTPScheme(t *testing.T) {
	if _, err := downloadAndParseM3U(context.Background(), "file:///etc/lankeeper/router.yaml"); err == nil {
		t.Error("a file:// playlist URL was accepted")
	}
	if _, err := downloadBlocklist(context.Background(), "file:///etc/shadow"); err == nil {
		t.Error("a file:// blocklist URL was accepted")
	}
	if httpProbe(context.Background(), "file:///etc/shadow", 204) {
		t.Error("a file:// probe URL reported success")
	}
}
