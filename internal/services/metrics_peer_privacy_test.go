package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// wgDumpAgent answers `wg show wg0 dump` with one live peer row and
// nothing else, so the collector has real input to work from.
type wgDumpAgent struct{ pubKey string }

func (a *wgDumpAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
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

	var stdout string
	if req.Cmd == "wg" && len(req.Args) > 0 && req.Args[0] == "show" {
		// Interface row first, then one 8-field peer row.
		stdout = "privkey\tifacepub\t51820\toff\n" +
			a.pubKey + "\tpsk\t203.0.113.5:51820\t10.10.11.2/32\t9999999999\t4096\t8192\t25\n"
	}
	return json.Marshal(struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exitCode"`
	}{Stdout: stdout})
}

// TestRoadWarriorPeerNameIsHashed is the regression test. The exposition
// emitted the operator-chosen peer name verbatim on six series covering
// presence, handshake age and traffic volume, on an endpoint that
// deliberately carries no authentication. Unlike a LAN client, a remote
// VPN peer is not otherwise observable from the local segment, so there
// was no ARP or mDNS equivalent that already leaked it.
func TestRoadWarriorPeerNameIsHashed(t *testing.T) {
	const peerName = "alice-laptop"
	const pubKey = "fakePeerPubKeyAABBCCDDEEFF00112233445566="

	netutil.SetAgentClient(&wgDumpAgent{pubKey: pubKey})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	cfg.VPN.Server.Enabled = true
	cfg.VPN.Server.Peers = []config.WGServerPeer{{Name: peerName, PublicKey: pubKey}}
	svc := &MetricsService{cfg: cfg}

	peers := svc.collectRoadWarriorPeers(context.Background())
	if len(peers) != 1 {
		t.Fatalf("collected %d peers, want 1", len(peers))
	}
	if peers[0].Name == peerName {
		t.Error("the peer name reached the metric verbatim")
	}
	if peers[0].Name != peerHash(peerName) {
		t.Errorf("Name = %q, want the hashed form", peers[0].Name)
	}
	// The traffic figures must survive; only the identifier changes.
	if peers[0].RxBytes != 4096 || peers[0].TxBytes != 8192 {
		t.Errorf("counters were lost: rx=%d tx=%d", peers[0].RxBytes, peers[0].TxBytes)
	}
}

// TestS2SPeerNameIsHashed covers the second family, which takes the same
// name from the configured entry.
func TestS2SPeerNameIsHashed(t *testing.T) {
	const peerName = "branch-istanbul"

	netutil.SetAgentClient(&wgDumpAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	cfg.VPN.Server.Peers = []config.WGServerPeer{{Name: peerName, IsSiteToSite: true}}
	svc := &MetricsService{cfg: cfg, vpn: NewVPNService(cfg)}

	peers := svc.collectS2SPeers(context.Background())
	if len(peers) != 1 {
		t.Fatalf("collected %d peers, want 1", len(peers))
	}
	if peers[0].Name == peerName {
		t.Error("the site name reached the metric verbatim")
	}
	if peers[0].Name != peerHash(peerName) {
		t.Errorf("Name = %q, want the hashed form", peers[0].Name)
	}
}

// TestNoPeerNameSurvivesIntoTheExposition is the end-to-end check on the
// rendered text, which is what a scraper actually receives.
func TestNoPeerNameSurvivesIntoTheExposition(t *testing.T) {
	const peerName = "alice-laptop"
	const pubKey = "fakePeerPubKeyAABBCCDDEEFF00112233445566="

	netutil.SetAgentClient(&wgDumpAgent{pubKey: pubKey})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	cfg.VPN.Server.Enabled = true
	cfg.VPN.Server.Peers = []config.WGServerPeer{{Name: peerName, PublicKey: pubKey}}
	svc := &MetricsService{cfg: cfg}

	snap := MetricsSnapshot{WGPeers: svc.collectRoadWarriorPeers(context.Background())}

	var sb strings.Builder
	if err := snap.Write(&sb); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := sb.String()

	if !strings.Contains(out, "lankeeper_wireguard_peer_online") {
		t.Fatal("the peer family is absent; the test proves nothing")
	}
	if strings.Contains(out, peerName) {
		t.Errorf("the peer name appears in the exposition:\n%s", out)
	}
}

// TestPeerHashIsStableAndDistinct keeps each peer a usable series. The
// expected values are pinned rather than recomputed: a label that
// changes between releases silently breaks continuity for every series
// a scraper has already recorded, which is worse than a rename.
func TestPeerHashIsStableAndDistinct(t *testing.T) {
	pinned := map[string]string{
		"alice": "522b276a",
		"bob":   "48181acd",
	}
	for name, want := range pinned {
		if got := peerHash(name); got != want {
			t.Errorf("peerHash(%q) = %q, want %q; existing series would break", name, got, want)
		}
	}
	if peerHash("alice") == peerHash("bob") {
		t.Error("two distinct peers produced the same label")
	}
}
