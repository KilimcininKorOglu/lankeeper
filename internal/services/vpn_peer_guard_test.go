package services

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// wgKeyAgent answers the two wg key commands AddPeer issues, so the
// test exercises the real allocation path rather than a stub.
type wgKeyAgent struct {
	mu    sync.Mutex
	calls int
}

func (a *wgKeyAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return []byte(`{}`), nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var req struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.calls++
	n := a.calls
	a.mu.Unlock()

	// wg genkey / pubkey / genpsk all return a base64 blob; the value
	// only has to be distinct per call so two peers do not collide.
	out := strings.Repeat("k", 42) + string(rune('A'+n%26)) + "="
	return json.Marshal(struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exitCode"`
	}{Stdout: out + "\n"})
}

func (a *wgKeyAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// newPeerGuardService builds a service whose LAN is 10.10.10.0/24 and
// whose tunnel network is 10.10.11.0/24, matching the shipped defaults.
func newPeerGuardService(t *testing.T) (*VPNService, *wgKeyAgent) {
	t.Helper()
	agent := &wgKeyAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan0", Device: "eth1", Role: "lan", Address: "10.10.10.1/24"},
	}
	cfg.VPN.Server.Address = "10.10.11.1/24"
	cfg.VPN.Server.Peers = nil
	return NewVPNService(cfg), agent
}

// TestAddPeerRejectsASubnetOverlappingTheLAN is the regression test. The
// manual Add Peer form validated only that each remote subnet parsed as
// a CIDR and wrote it straight into AllowedIPs. The invite wizard
// guarded exactly this case and the guard was never back-ported, so
// through the ordinary UI an operator could hand a peer the router's own
// LAN. The rendered server config re-validates nothing, so WireGuard
// then treats that peer as authoritative for LAN-address traffic.
func TestAddPeerRejectsASubnetOverlappingTheLAN(t *testing.T) {
	svc, _ := newPeerGuardService(t)

	_, _, err := svc.AddPeer(context.Background(), "rogue", true, []string{"10.10.10.0/24"}, "")
	if err == nil {
		t.Fatal("a peer claiming the LAN subnet was accepted")
	}
	if !errors.Is(err, ErrPeerSubnetConflict) {
		t.Errorf("got %v, want ErrPeerSubnetConflict", err)
	}
	if n := len(svc.cfg.VPN.Server.Peers); n != 0 {
		t.Errorf("%d peers were persisted despite the rejection", n)
	}
}

// TestAddPeerRejectsASupernetOfTheLAN covers the containment direction
// the wizard's check handles: a wider prefix swallows the LAN just as
// effectively as an exact match.
func TestAddPeerRejectsASupernetOfTheLAN(t *testing.T) {
	svc, _ := newPeerGuardService(t)

	if _, _, err := svc.AddPeer(context.Background(), "rogue", true, []string{"10.0.0.0/8"}, ""); !errors.Is(err, ErrPeerSubnetConflict) {
		t.Fatalf("got %v, want ErrPeerSubnetConflict", err)
	}
}

// TestAddPeerRejectsTheTunnelNetwork keeps the VPN server's own address
// range covered, which localSubnets includes alongside the LAN.
func TestAddPeerRejectsTheTunnelNetwork(t *testing.T) {
	svc, _ := newPeerGuardService(t)

	if _, _, err := svc.AddPeer(context.Background(), "rogue", true, []string{"10.10.11.0/24"}, ""); !errors.Is(err, ErrPeerSubnetConflict) {
		t.Fatalf("got %v, want ErrPeerSubnetConflict", err)
	}
}

// TestAddPeerRejectsBeforeSpendingPrivilegedCalls keeps the guard ahead
// of the keypair generation, which shells out through the root agent.
func TestAddPeerRejectsBeforeSpendingPrivilegedCalls(t *testing.T) {
	svc, agent := newPeerGuardService(t)

	_, _, _ = svc.AddPeer(context.Background(), "rogue", true, []string{"10.10.10.0/24"}, "")
	if n := agent.count(); n != 0 {
		t.Errorf("a rejected peer still ran %d privileged commands", n)
	}
}

// TestAddPeerAcceptsANonOverlappingSubnet keeps the guard from refusing
// the legitimate site-to-site case it exists to permit.
func TestAddPeerAcceptsANonOverlappingSubnet(t *testing.T) {
	svc, _ := newPeerGuardService(t)

	peer, _, err := svc.AddPeer(context.Background(), "branch", true, []string{"192.168.5.0/24"}, "203.0.113.5:51820")
	if err != nil {
		t.Fatalf("a non-overlapping subnet was refused: %v", err)
	}
	if !strings.Contains(peer.AllowedIPs, "192.168.5.0/24") {
		t.Errorf("AllowedIPs = %q, want the remote subnet included", peer.AllowedIPs)
	}
}

// TestAddPeerRejectsADuplicateName is the second half. Every name-keyed
// lookup stops at the first match, so two peers sharing a name make
// removal and the client lookup ambiguous. The wizard rejected this and
// the manual form appended unconditionally.
func TestAddPeerRejectsADuplicateName(t *testing.T) {
	svc, _ := newPeerGuardService(t)
	ctx := context.Background()

	if _, _, err := svc.AddPeer(ctx, "laptop", false, nil, ""); err != nil {
		t.Fatalf("first AddPeer: %v", err)
	}

	_, _, err := svc.AddPeer(ctx, "laptop", false, nil, "")
	if err == nil {
		t.Fatal("a duplicate peer name was accepted")
	}
	if !errors.Is(err, ErrPeerNameInUse) {
		t.Errorf("got %v, want ErrPeerNameInUse", err)
	}
	if n := len(svc.cfg.VPN.Server.Peers); n != 1 {
		t.Errorf("peer list holds %d entries, want 1", n)
	}
}

// TestAddPeerRejectsANamePendingFromTheWizard covers the cross-path
// case: a wizard invite reserves the name before its peer is finalized,
// and the manual form must see that reservation.
func TestAddPeerRejectsANamePendingFromTheWizard(t *testing.T) {
	svc, _ := newPeerGuardService(t)
	svc.cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "branch", Pending: true, IsSiteToSite: true},
	}

	if _, _, err := svc.AddPeer(context.Background(), "branch", false, nil, ""); !errors.Is(err, ErrPeerNameInUse) {
		t.Fatalf("got %v, want ErrPeerNameInUse", err)
	}
}

// TestConcurrentAddPeerKeepsNamesUnique holds the check inside the same
// lock as the append, so two simultaneous adds cannot both pass it.
func TestConcurrentAddPeerKeepsNamesUnique(t *testing.T) {
	svc, _ := newPeerGuardService(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = svc.AddPeer(ctx, "laptop", false, nil, "")
		}()
	}
	wg.Wait()

	if n := len(svc.cfg.VPN.Server.Peers); n != 1 {
		t.Errorf("8 concurrent adds of one name produced %d peers, want 1", n)
	}
}

// TestWizardStillRejectsADuplicateName keeps the shared helper from
// regressing the path that already had the check.
func TestWizardStillRejectsADuplicateName(t *testing.T) {
	svc := newS2STestService(t)
	// The wizard generates the preshared key before it checks the name,
	// so the agent has to answer even for a request that gets rejected.
	netutil.SetAgentClient(&wgKeyAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc.cfg.VPN.Server.Peers = []config.WGServerPeer{{Name: "branch"}}

	_, _, err := svc.CreateS2SInvite(context.Background(), "branch", "Branch", "203.0.113.5:51820", []string{"192.168.5.0/24"})
	if err == nil {
		t.Fatal("the wizard accepted a duplicate name")
	}
	if !errors.Is(err, ErrPeerNameInUse) {
		t.Errorf("got %v, want ErrPeerNameInUse", err)
	}
}
