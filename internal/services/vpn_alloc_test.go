package services_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// wgKeyAgent answers the three `wg` key commands AddPeer issues so the
// allocation path can run without a real WireGuard binary. Each call
// returns a distinct value, otherwise every peer would share a public
// key and the collision under test would be masked by a key collision.
type wgKeyAgent struct {
	mu sync.Mutex
	n  int
}

func (a *wgKeyAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return nil, fmt.Errorf("unexpected method %q", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Cmd != "wg" || len(p.Args) == 0 {
		return nil, fmt.Errorf("unexpected command %q %v", p.Cmd, p.Args)
	}

	a.mu.Lock()
	a.n++
	out := fmt.Sprintf("%s-%d", p.Args[0], a.n)
	a.mu.Unlock()

	res, err := json.Marshal(struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exitCode"`
	}{Stdout: out})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// newAllocTestVPN wires a VPN service against the fake agent. The agent
// client is process-global, so these tests must never run in parallel.
func newAllocTestVPN(t *testing.T) (*services.VPNService, *config.Config) {
	t.Helper()
	netutil.SetAgentClient(&wgKeyAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	dir := t.TempDir()
	// AddPeer stores the peer's private key, so persisting now needs
	// the credential encryption key, and its default location is under
	// /var/lib where a test cannot write.
	t.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(dir, "config.key"))

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(dir, "router.yaml"))
	cfg.VPN.Server.Enabled = true
	cfg.VPN.Server.ListenPort = 51820
	cfg.VPN.Server.Address = "10.10.11.1/24"
	return services.NewVPNService(cfg), cfg
}

// tunnelIP returns the peer's own /32, which is always the first entry
// of AllowedIPs. A site-to-site peer appends remote subnets after it.
func tunnelIP(peer config.WGServerPeer) string {
	return strings.TrimSpace(strings.SplitN(peer.AllowedIPs, ",", 2)[0])
}

// TestAddPeerReusesTheAddressFreedByARemovedPeer is the regression test.
// AddPeer derived the address from len(Peers)+2, so after removing a
// peer from the middle of the slice the count fell back to a value that
// an existing peer still held. The next peer was handed a duplicate /32
// and both tunnels broke.
func TestAddPeerReusesTheAddressFreedByARemovedPeer(t *testing.T) {
	svc, cfg := newAllocTestVPN(t)
	ctx := context.Background()

	for _, name := range []string{"phone", "laptop", "tablet"} {
		if _, _, err := svc.AddPeer(ctx, name, false, nil, ""); err != nil {
			t.Fatalf("add peer %q: %v", name, err)
		}
	}

	want := []string{"10.10.11.2/32", "10.10.11.3/32", "10.10.11.4/32"}
	for i, w := range want {
		if got := tunnelIP(cfg.VPN.Server.Peers[i]); got != w {
			t.Fatalf("peer %d: got %s, want %s", i, got, w)
		}
	}

	// Remove the middle peer. .3 is now free, .4 is still in use.
	if err := svc.RemovePeer("laptop"); err != nil {
		t.Fatalf("remove peer: %v", err)
	}

	if _, _, err := svc.AddPeer(ctx, "desktop", false, nil, ""); err != nil {
		t.Fatalf("add peer after removal: %v", err)
	}

	seen := map[string]string{}
	for _, p := range cfg.VPN.Server.Peers {
		ip := tunnelIP(p)
		if other, dup := seen[ip]; dup {
			t.Fatalf("peers %q and %q both hold %s", other, p.Name, ip)
		}
		seen[ip] = p.Name
	}

	if got := seen["10.10.11.3/32"]; got != "desktop" {
		t.Errorf("freed address went to %q, want desktop", got)
	}
}

// TestAddPeerKeepsTheTunnelIPFirstForSiteToSite guards the branch that
// concatenates remote subnets: the allocated /32 must stay the leading
// entry, because nextTunnelIP reads only that first element when it
// works out which addresses are taken.
func TestAddPeerKeepsTheTunnelIPFirstForSiteToSite(t *testing.T) {
	svc, cfg := newAllocTestVPN(t)
	ctx := context.Background()

	if _, _, err := svc.AddPeer(ctx, "branch", true, []string{"192.168.50.0/24"}, "vpn.example:51820"); err != nil {
		t.Fatalf("add site-to-site peer: %v", err)
	}
	if _, _, err := svc.AddPeer(ctx, "phone", false, nil, ""); err != nil {
		t.Fatalf("add peer: %v", err)
	}

	if got, want := cfg.VPN.Server.Peers[0].AllowedIPs, "10.10.11.2/32, 192.168.50.0/24"; got != want {
		t.Errorf("site-to-site AllowedIPs: got %q, want %q", got, want)
	}
	if got := tunnelIP(cfg.VPN.Server.Peers[1]); got != "10.10.11.3/32" {
		t.Errorf("second peer: got %s, want 10.10.11.3/32", got)
	}
}

// TestAddPeerAllocatesUniqueAddressesConcurrently covers the second half
// of the defect: reading the free slot and appending the peer have to
// happen under one lock, otherwise two callers derive the same address.
func TestAddPeerAllocatesUniqueAddressesConcurrently(t *testing.T) {
	svc, cfg := newAllocTestVPN(t)
	ctx := context.Background()

	const peers = 8
	var wg sync.WaitGroup
	errs := make(chan error, peers)
	for i := range peers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, _, err := svc.AddPeer(ctx, fmt.Sprintf("peer-%d", i), false, nil, ""); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent add: %v", err)
	}

	seen := map[string]string{}
	for _, p := range cfg.VPN.Server.Peers {
		ip := tunnelIP(p)
		if other, dup := seen[ip]; dup {
			t.Fatalf("peers %q and %q both hold %s", other, p.Name, ip)
		}
		seen[ip] = p.Name
	}
	if len(seen) != peers {
		t.Errorf("allocated %d distinct addresses, want %d", len(seen), peers)
	}
}
