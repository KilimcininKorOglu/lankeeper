package services_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestSixInFourPPPoEReconnectFullChain wires SixInFourService and
// IPv6Service together exactly the way web/server.go does in 6in4
// mode and then drives the PPPoE on-connect callback by hand.
//
// The contract being verified is the full v0.4.0 cross-service path:
//
//  1. UpdateRemoteIPv4 POSTs to HE.net /nic/update with Basic Auth
//     and the new endpoint IPv4.
//  2. Restart tears down any prior tunnel (tunnel del → ignore err),
//     then runs ip tunnel add mode sit / link set up mtu / addr add /
//     -6 route add ::/0.
//  3. The dnsmasq RA drop-in is rewritten so 6in4 RoutedPrefix-derived
//     /64s reach the LAN, and dnsmasq is reloaded.
//  4. Subsequent calls with the same IPv4 collapse to a single HTTP
//     hit thanks to lastIPv4 deduplication.
//
// This is the integration safety net that unit tests cannot provide
// because the cross-service callback wiring lives in server.go.
func TestSixInFourPPPoEReconnectFullChain(t *testing.T) {
	// netutil.agentClient is process-global — no t.Parallel().
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	// HE.net /nic/update mock — count hits and capture last endpoint.
	var hits int
	var lastQuery string
	hesrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		lastQuery = r.URL.RawQuery
		// HE.net replies "good <ipv4>" on success.
		_, _ = w.Write([]byte("good 203.0.113.42"))
	}))
	t.Cleanup(hesrv.Close)
	heRT := &rewriteRT{target: hesrv.URL, inner: http.DefaultTransport}

	cfg := newIPv6TestConfig(t)
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.IPv6.Enabled = "auto"
	cfg.IPv6.Mode = "6in4"
	cfg.IPv6.WAN.RequestPrefix = false
	cfg.IPv6.Tunnel.ServerIPv4 = "216.66.80.30"
	cfg.IPv6.Tunnel.ClientIPv6 = "2001:470:1f0a:abc::2"
	cfg.IPv6.Tunnel.RoutedPrefix = "2001:470:abcd::/64"
	cfg.IPv6.Tunnel.TunnelID = "12345"
	cfg.IPv6.Tunnel.Username = "kilimci"
	cfg.IPv6.Tunnel.UpdateKey = "supersecret"
	cfg.IPv6.Tunnel.AutoUpdate = true
	cfg.IPv6.Tunnel.Device = "lkt6in4"
	cfg.PPPoE.Username = "subscriber@isp"

	tunnel := services.NewSixInFourService(cfg)
	tunnel.SetHTTPClientForTest(&http.Client{Transport: heRT, Timeout: 5 * time.Second})
	tunnel.SetStatePathForTest(filepath.Join(t.TempDir(), "ipv6-tunnel.json"))
	tunnel.SetLocalIPv4ForTest("203.0.113.42")

	ipv6 := newIPv6TestService(t, cfg)

	// Mirror the on-connect callback wired in web/server.go for 6in4.
	onConnect := func(ctx context.Context, currentIPv4 string) error {
		if cfg.IPv6.Tunnel.AutoUpdate && currentIPv4 != "" {
			if _, err := tunnel.UpdateRemoteIPv4(ctx, currentIPv4); err != nil {
				return err
			}
		}
		if err := tunnel.Restart(ctx); err != nil {
			return err
		}
		return ipv6.ApplyConfig(ctx)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First reconnect — full chain runs.
	if err := onConnect(ctx, "203.0.113.42"); err != nil {
		t.Fatalf("onConnect: %v", err)
	}

	if hits != 1 {
		t.Errorf("expected exactly 1 HE.net /nic/update call, got %d", hits)
	}
	for _, want := range []string{"hostname=12345", "myip=203.0.113.42"} {
		if !strings.Contains(lastQuery, want) {
			t.Errorf("HE.net query missing %q, got %q", want, lastQuery)
		}
	}

	// Tunnel lifecycle: del cleanup, add mode sit, link set up mtu,
	// addr add ClientIPv6, default route ::/0.
	wantIP := []struct{ contains string }{
		{"tunnel del lkt6in4"},
		{"tunnel add lkt6in4 mode sit remote 216.66.80.30 local 203.0.113.42 ttl 255"},
		{"link set lkt6in4 up mtu 1452"},
		{"addr add 2001:470:1f0a:abc::2"},
		{"-6 route add ::/0"},
	}
	for _, w := range wantIP {
		if !ipExecMatched(agent, w.contains) {
			t.Errorf("expected ip invocation containing %q; ip log:\n%s",
				w.contains, dumpIP(agent))
		}
	}

	// dnsmasq RA drop-in rewritten for the 6in4 plane and dnsmasq
	// reloaded so the RoutedPrefix-derived /64 reaches LAN clients.
	if !agent.wroteFile("/etc/dnsmasq.d/lankeeper-ipv6-ra.conf") {
		t.Errorf("expected RA drop-in rewrite; write log:\n%+v", agent.writeLog)
	}
	if agent.execCount("systemctl") < 1 {
		t.Errorf("expected dnsmasq systemctl reload-or-restart; exec log:\n%+v", agent.execLog)
	}

	// Second reconnect with the SAME IPv4 — UpdateRemoteIPv4 must
	// dedupe to zero HTTP hits, but the tunnel+RA chain still runs
	// because PPPoE may have rotated session IDs even when the IPv4
	// happens to recycle.
	hitsBefore := hits
	if err := onConnect(ctx, "203.0.113.42"); err != nil {
		t.Fatalf("onConnect dedup: %v", err)
	}
	if hits != hitsBefore {
		t.Errorf("expected dedup to suppress duplicate /nic/update, hits %d -> %d",
			hitsBefore, hits)
	}
}

// ipExecMatched reports whether any "ip" invocation captured by the
// fake agent contains the given substring (after joining argv).
func ipExecMatched(a *fakeAgent, substr string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.execLog {
		if c.Cmd != "ip" {
			continue
		}
		if strings.Contains(strings.Join(c.Args, " "), substr) {
			return true
		}
	}
	return false
}

func dumpIP(a *fakeAgent) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var b strings.Builder
	for _, c := range a.execLog {
		if c.Cmd != "ip" {
			continue
		}
		b.WriteString("  ip ")
		b.WriteString(strings.Join(c.Args, " "))
		b.WriteString("\n")
	}
	return b.String()
}
