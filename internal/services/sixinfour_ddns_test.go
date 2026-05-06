package services_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// newDDNSTestServer wraps an httptest.Server that records the last
// request and returns the body the test expects. The handler
// validates Basic Auth so we also exercise the auth header path.
func newDDNSTestServer(t *testing.T, body string, wantUser, wantPass string) (*httptest.Server, *http.Request) {
	t.Helper()
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		u, p, ok := r.BasicAuth()
		if !ok || u != wantUser || p != wantPass {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("badauth"))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

// rewriteSixInFourEndpoint redirects HE.net's /nic/update URL to the
// httptest server. Cannot poke the const directly so we swap the
// HTTP transport to a custom RoundTripper that rewrites the host.
type rewriteRT struct {
	target string
	inner  http.RoundTripper
}

func (r *rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "tunnelbroker.net") {
		req.URL.Host = strings.TrimPrefix(r.target, "http://")
		req.URL.Scheme = "http"
	}
	return r.inner.RoundTrip(req)
}

func TestSixInFourDDNSGoodResponse(t *testing.T) {
	srv, _ := newDDNSTestServer(t, "good 198.51.100.7", "operator", "TESTKEY")

	cfg := newSixInFourTestConfig(t)
	cfg.IPv6.Tunnel.Username = "operator"
	cfg.IPv6.Tunnel.UpdateKey = "TESTKEY"
	svc := services.NewSixInFourService(cfg)
	svc.SetStatePathForTest(filepath.Join(t.TempDir(), "ipv6-tunnel.json"))
	svc.SetHTTPClientForTest(&http.Client{
		Transport: &rewriteRT{target: srv.URL, inner: http.DefaultTransport},
	})

	res, err := svc.UpdateRemoteIPv4(context.Background(), "198.51.100.7")
	if err != nil {
		t.Fatalf("UpdateRemoteIPv4: %v", err)
	}
	if res.Code != "good" {
		t.Errorf("Code = %q, want good", res.Code)
	}
	if res.IP != "198.51.100.7" {
		t.Errorf("IP = %q, want 198.51.100.7", res.IP)
	}
}

func TestSixInFourDDNSBadAuthDoesNotCacheIP(t *testing.T) {
	srv, _ := newDDNSTestServer(t, "ignored", "right", "right")

	cfg := newSixInFourTestConfig(t)
	cfg.IPv6.Tunnel.Username = "wrong"
	cfg.IPv6.Tunnel.UpdateKey = "wrong"
	svc := services.NewSixInFourService(cfg)
	svc.SetStatePathForTest(filepath.Join(t.TempDir(), "ipv6-tunnel.json"))
	svc.SetHTTPClientForTest(&http.Client{
		Transport: &rewriteRT{target: srv.URL, inner: http.DefaultTransport},
	})

	res1, err := svc.UpdateRemoteIPv4(context.Background(), "198.51.100.7")
	if err != nil {
		t.Fatalf("UpdateRemoteIPv4 first call: %v", err)
	}
	if res1.Code != "badauth" {
		t.Errorf("first call Code = %q, want badauth", res1.Code)
	}

	// Same IP again — must NOT short-circuit to "nochg" since the
	// previous call was an auth failure. The service must retry.
	res2, err := svc.UpdateRemoteIPv4(context.Background(), "198.51.100.7")
	if err != nil {
		t.Fatalf("UpdateRemoteIPv4 second call: %v", err)
	}
	if res2.Code == "nochg" && res2.Raw == "dedup" {
		t.Errorf("second badauth dedup'd to %+v; want fresh attempt", res2)
	}
}

func TestSixInFourDDNSDedupesIdenticalIP(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte("good 198.51.100.7"))
	}))
	t.Cleanup(srv.Close)

	cfg := newSixInFourTestConfig(t)
	svc := services.NewSixInFourService(cfg)
	svc.SetStatePathForTest(filepath.Join(t.TempDir(), "ipv6-tunnel.json"))
	svc.SetHTTPClientForTest(&http.Client{
		Transport: &rewriteRT{target: srv.URL, inner: http.DefaultTransport},
	})

	for i := 0; i < 3; i++ {
		_, err := svc.UpdateRemoteIPv4(context.Background(), "198.51.100.7")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call (rest dedup'd), got %d", calls)
	}
}

func TestSixInFourDDNSSendsBasicAuthHeader(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("good 1.2.3.4"))
	}))
	t.Cleanup(srv.Close)

	cfg := newSixInFourTestConfig(t)
	cfg.IPv6.Tunnel.Username = "u-name"
	cfg.IPv6.Tunnel.UpdateKey = "k3y"
	svc := services.NewSixInFourService(cfg)
	svc.SetStatePathForTest(filepath.Join(t.TempDir(), "ipv6-tunnel.json"))
	svc.SetHTTPClientForTest(&http.Client{
		Transport: &rewriteRT{target: srv.URL, inner: http.DefaultTransport},
	})

	if _, err := svc.UpdateRemoteIPv4(context.Background(), "1.2.3.4"); err != nil {
		t.Fatalf("UpdateRemoteIPv4: %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("u-name:k3y"))
	if seenAuth != want {
		t.Errorf("Authorization header = %q, want %q", seenAuth, want)
	}
}

func TestSixInFourDDNSEmptyConfigReturnsError(t *testing.T) {
	cfg := newSixInFourTestConfig(t)
	cfg.IPv6.Tunnel.Username = "" // strip credentials
	cfg.IPv6.Tunnel.UpdateKey = ""
	svc := services.NewSixInFourService(cfg)

	_, err := svc.UpdateRemoteIPv4(context.Background(), "1.2.3.4")
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	if !strings.Contains(err.Error(), "TunnelID/Username/UpdateKey") {
		t.Errorf("error message = %v", err)
	}
}

func TestSixInFourDDNSEmptyIPReturnsError(t *testing.T) {
	cfg := newSixInFourTestConfig(t)
	svc := services.NewSixInFourService(cfg)
	_, err := svc.UpdateRemoteIPv4(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty IP")
	}
}
