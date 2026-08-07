package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// newS2STestService builds a minimal VPNService whose config has a
// SessionSecret (required for token signing) and one LAN interface
// so localSubnets() returns something. SaveToFile is wired to a
// TempDir so persist() doesn't blow up on missing path.
func newS2STestService(t *testing.T) *VPNService {
	t.Helper()
	// Token signing needs a key file, and the production path is under
	// /var/lib where the test process cannot write.
	useTempS2SKey(t)
	cfg := config.DefaultConfig()
	cfg.System.SessionSecret = "test-secret-32-bytes-or-thereabouts"
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan0", Device: "eth1", Role: "lan", Address: "10.10.10.1/24"},
	}
	cfg.VPN.Server.PublicKey = "fakeServerPubKeyAABBCCDDEEFF112233"
	cfg.VPN.Server.Address = "10.10.11.1/24"
	cfg.VPN.Server.ListenPort = 51820
	return NewVPNService(cfg)
}

func TestSignVerifyTokenRoundTrip(t *testing.T) {
	svc := newS2STestService(t)
	payload := S2SInvite{
		Version:  inviteSchemaVersion,
		Kind:     tokenKindInvite,
		Name:     "tester",
		Endpoint: "1.2.3.4:51820",
	}
	tok, err := svc.signToken(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	body, err := svc.verifyToken(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(string(body), "tester") {
		t.Errorf("verified body missing payload: %s", body)
	}
}

func TestVerifyTokenRejectsTampering(t *testing.T) {
	svc := newS2STestService(t)
	tok, err := svc.signToken(S2SInvite{Version: 1, Kind: tokenKindInvite, Name: "n"})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Flip a byte in the body half.
	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected token shape: %s", tok)
	}
	tampered := parts[0][:len(parts[0])-1] + "X" + "." + parts[1]
	if _, err := svc.verifyToken(tampered); !errors.Is(err, ErrInviteSignature) && !errors.Is(err, ErrInviteMalformed) {
		t.Errorf("expected signature/malformed error, got: %v", err)
	}
}

func TestParseInviteRejectsExpired(t *testing.T) {
	svc := newS2STestService(t)
	tok, err := svc.signToken(S2SInvite{
		Version:   inviteSchemaVersion,
		Kind:      tokenKindInvite,
		Name:      "n",
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := svc.ParseInviteToken(tok); !errors.Is(err, ErrInviteExpired) {
		t.Errorf("expected ErrInviteExpired, got: %v", err)
	}
}

func TestParseInviteRejectsSchemaMismatch(t *testing.T) {
	svc := newS2STestService(t)
	tok, _ := svc.signToken(S2SInvite{Version: 99, Kind: tokenKindInvite, Name: "n"})
	if _, err := svc.ParseInviteToken(tok); !errors.Is(err, ErrInviteSchema) {
		t.Errorf("expected ErrInviteSchema, got: %v", err)
	}
}

func TestParseInviteRejectsAckTokenAndViceVersa(t *testing.T) {
	svc := newS2STestService(t)
	ackTok, _ := svc.signToken(S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "n"})
	if _, err := svc.ParseInviteToken(ackTok); !errors.Is(err, ErrInviteMalformed) {
		t.Errorf("invite parser should reject ack token, got: %v", err)
	}
	invTok, _ := svc.signToken(S2SInvite{Version: inviteSchemaVersion, Kind: tokenKindInvite, Name: "n"})
	if _, err := svc.ParseAckToken(invTok); !errors.Is(err, ErrInviteMalformed) {
		t.Errorf("ack parser should reject invite token, got: %v", err)
	}
}

func TestSubnetsConflictDetectsOverlap(t *testing.T) {
	svc := newS2STestService(t)
	if _, ok := svc.subnetsConflict([]string{"10.10.10.0/24"}); !ok {
		t.Error("identical subnet should be reported as conflict")
	}
	if _, ok := svc.subnetsConflict([]string{"192.168.5.0/24"}); ok {
		t.Error("disjoint subnet should not conflict")
	}
}

func TestNextTunnelIPSkipsAllocated(t *testing.T) {
	svc := newS2STestService(t)
	svc.cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "a", AllowedIPs: "10.10.11.2/32"},
		{Name: "b", AllowedIPs: "10.10.11.3/32, 10.20.0.0/24"},
	}
	got, err := svc.nextTunnelIP()
	if err != nil {
		t.Fatalf("nextTunnelIP: %v", err)
	}
	if got != "10.10.11.4/32" {
		t.Errorf("expected 10.10.11.4/32, got %s", got)
	}
}

func TestGatewayOfSubnet(t *testing.T) {
	cases := map[string]string{
		"192.168.5.0/24": "192.168.5.1",
		"10.0.0.0/16":    "10.0.0.1",
	}
	for in, want := range cases {
		got, err := gatewayOfSubnet(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

// TestS2SHandshakeFlow simulates the full A→B→A handshake using
// two independent VPNService instances backed by independent
// configs. No agent is wired; CreateS2SInvite needs to call wg
// genpsk which is supplied via a controlled netutil mock below.
//
// We bypass the agent path by sharing the SessionSecret: both
// services use the same secret so the ack token signed by B
// verifies correctly on A. This mirrors how the operator pastes
// the secret-bearing payload between routers — except in
// production each router has its OWN secret and the ack is signed
// with the joining side's secret. For v1 we accept that pattern:
// the originator verifies an ack signed under ITS OWN secret only
// when the joining side is also a LANKeeper that received the
// secret out-of-band. Cross-implementation acks (vanilla wg) skip
// this verification entirely (operator pastes the public key
// directly, no signature).
//
// This test focuses on the data-flow, not the cross-org auth model.
func TestS2SHandshakeFlow_LocalCryptoOnly(t *testing.T) {
	a := newS2STestService(t)
	// Put the originator on LAN .10 and the "joining side" on a
	// different RFC1918 block so subnet conflict logic doesn't fire.
	b := newS2STestService(t)
	b.cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan0", Device: "eth1", Role: "lan", Address: "192.168.5.1/24"},
	}
	b.cfg.VPN.Server.Address = "10.10.11.1/24"
	// Share the secret so the ack signed by B verifies on A.
	b.cfg.System.SessionSecret = a.cfg.System.SessionSecret

	// CreateS2SInvite calls GeneratePresharedKey via netutil.RunSimple
	// → no agent in tests means it falls back to local exec, which
	// requires the wg binary. Skip if not available.
	if _, err := a.GeneratePresharedKey(context.Background()); err != nil {
		t.Skipf("wg binary not available: %v", err)
	}

	// A side: issue invite for B (B will announce 192.168.5.0/24)
	tok, peer, err := a.CreateS2SInvite(context.Background(), "siteB", "Istanbul", "203.0.113.5:51820", []string{"192.168.5.0/24"})
	if err != nil {
		t.Fatalf("CreateS2SInvite: %v", err)
	}
	if !peer.Pending {
		t.Errorf("freshly issued peer should be Pending=true")
	}
	if peer.PublicKey != "" {
		t.Errorf("Pending peer must not have PublicKey set yet")
	}

	// B side: consume invite → returns ack token + B's pubkey.
	ack, bPub, joinedPeer, err := b.ConsumeInvite(context.Background(), tok)
	if err != nil {
		t.Fatalf("ConsumeInvite: %v", err)
	}
	if joinedPeer.Pending {
		t.Errorf("joined peer on B should not be Pending")
	}
	if bPub == "" {
		t.Errorf("ConsumeInvite must return B's public key")
	}

	// A side: finalize with B's ack token.
	finalized, err := a.FinalizeInvite(context.Background(), "siteB", ack)
	if err != nil {
		t.Fatalf("FinalizeInvite: %v", err)
	}
	if finalized.Pending {
		t.Errorf("Finalized peer should not be Pending")
	}
	if finalized.PublicKey != bPub {
		t.Errorf("Finalized PublicKey mismatch: %q vs %q", finalized.PublicKey, bPub)
	}
}

func TestGCExpiredInvitesReapsOldPendings(t *testing.T) {
	svc := newS2STestService(t)
	svc.cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "active", PublicKey: "abc", IsSiteToSite: true},
		{Name: "fresh", Pending: true, InviteExpiresAt: time.Now().Add(time.Hour), IsSiteToSite: true},
		{Name: "stale", Pending: true, InviteExpiresAt: time.Now().Add(-time.Hour), IsSiteToSite: true},
	}
	if got := svc.GCExpiredInvites(); got != 1 {
		t.Errorf("expected 1 reaped, got %d", got)
	}
	names := make([]string, 0, len(svc.cfg.VPN.Server.Peers))
	for _, p := range svc.cfg.VPN.Server.Peers {
		names = append(names, p.Name)
	}
	want := []string{"active", "fresh"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("after GC peers = %v, want %v", names, want)
	}
}

func TestCancelInviteRemovesPendingOnly(t *testing.T) {
	svc := newS2STestService(t)
	svc.cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "active", PublicKey: "abc", IsSiteToSite: true},
		{Name: "pending", Pending: true, IsSiteToSite: true},
	}
	if err := svc.CancelInvite("pending"); err != nil {
		t.Fatalf("cancel pending: %v", err)
	}
	if len(svc.cfg.VPN.Server.Peers) != 1 {
		t.Errorf("pending peer should have been removed")
	}
	if err := svc.CancelInvite("active"); !errors.Is(err, ErrPeerNotPending) {
		t.Errorf("cancel of active peer should return ErrPeerNotPending, got: %v", err)
	}
	if err := svc.CancelInvite("nonexistent"); err != nil {
		t.Errorf("cancel of unknown peer should be idempotent, got: %v", err)
	}
}
