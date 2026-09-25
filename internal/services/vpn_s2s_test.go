package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// testWGKey returns a random value in WireGuard key format.
func testWGKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// genpskAgent answers `wg genpsk` with a fresh key, which is the only
// privileged command the invite path runs.
type genpskAgent struct{ t *testing.T }

func (a genpskAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return []byte(`{}`), nil
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
	if p.Cmd != "wg" || len(p.Args) == 0 || p.Args[0] != "genpsk" {
		return nil, errors.New("unexpected command: " + p.Cmd + " " + strings.Join(p.Args, " "))
	}
	return json.Marshal(map[string]any{"stdout": testWGKey(a.t) + "\n", "stderr": "", "exitCode": 0})
}

// useGenpskAgent wires the agent for the test and resets it afterwards.
func useGenpskAgent(t *testing.T) {
	t.Helper()
	netutil.SetAgentClient(genpskAgent{t: t})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
}

// newS2STestService builds a VPNService with one LAN interface and a
// server key pair, backed by its own config file.
func newS2STestService(t *testing.T) *VPNService {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan0", Device: "eth1", Role: "lan", Address: "10.10.10.1/24"},
	}
	cfg.VPN.Server.PublicKey = testWGKey(t)
	cfg.VPN.Server.Address = "10.10.11.1/24"
	cfg.VPN.Server.ListenPort = 51820
	return NewVPNService(cfg)
}

// validInvite returns an invite that passes every field check.
func validInvite(t *testing.T) *S2SInvite {
	t.Helper()
	return &S2SInvite{
		Version:         inviteSchemaVersion,
		Kind:            tokenKindInvite,
		Name:            "siteB",
		Endpoint:        "203.0.113.5:51820",
		PublicKey:       testWGKey(t),
		PresharedKey:    testWGKey(t),
		RemoteSubnets:   []string{"10.10.10.0/24"},
		ExpectedSubnets: []string{"192.168.5.0/24"},
		ExpiresAt:       time.Now().Add(time.Hour),
	}
}

func encodeTestInvite(t *testing.T, inv *S2SInvite) string {
	t.Helper()
	tok, err := encodeInvite(inv)
	if err != nil {
		t.Fatalf("encode invite: %v", err)
	}
	return tok
}

func TestInviteTokenRoundTrip(t *testing.T) {
	inv := validInvite(t)
	got, err := ParseInviteToken(encodeTestInvite(t, inv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Name != inv.Name || got.PublicKey != inv.PublicKey || got.PresharedKey != inv.PresharedKey {
		t.Errorf("round trip changed the invite: %+v", got)
	}
}

func TestParseInviteRejectsExpired(t *testing.T) {
	inv := validInvite(t)
	inv.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := ParseInviteToken(encodeTestInvite(t, inv)); !errors.Is(err, ErrInviteExpired) {
		t.Errorf("expected ErrInviteExpired, got: %v", err)
	}
}

func TestParseInviteRejectsSchemaMismatch(t *testing.T) {
	inv := validInvite(t)
	inv.Version = 1
	if _, err := ParseInviteToken(encodeTestInvite(t, inv)); !errors.Is(err, ErrInviteSchema) {
		t.Errorf("expected ErrInviteSchema, got: %v", err)
	}
}

func TestParseInviteRejectsAckTokenAndViceVersa(t *testing.T) {
	ackTok, err := signAck(&S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "n", PublicKey: testWGKey(t)}, testWGKey(t))
	if err != nil {
		t.Fatalf("sign ack: %v", err)
	}
	if _, err := ParseInviteToken(ackTok); !errors.Is(err, ErrInviteMalformed) {
		t.Errorf("invite parser should reject ack token, got: %v", err)
	}
	if _, _, _, err := ParseAckToken(encodeTestInvite(t, validInvite(t))); !errors.Is(err, ErrInviteMalformed) {
		t.Errorf("ack parser should reject invite token, got: %v", err)
	}
}

// TestParseInviteRejectsConfigInjection covers the fields the joining
// side writes into wgs0.conf. The invite is unsigned, so a newline in
// any of them would add lines, such as a PostUp command, to a file
// wg-quick runs as root.
func TestParseInviteRejectsConfigInjection(t *testing.T) {
	for label, mutate := range map[string]func(*S2SInvite){
		"name":          func(i *S2SInvite) { i.Name = "b\nPostUp = id" },
		"public key":    func(i *S2SInvite) { i.PublicKey = "abc\nPostUp = id" },
		"preshared key": func(i *S2SInvite) { i.PresharedKey = "short" },
		"endpoint":      func(i *S2SInvite) { i.Endpoint = "1.2.3.4:51820\nPostUp = id" },
		"endpoint port": func(i *S2SInvite) { i.Endpoint = "1.2.3.4:0" },
		"subnet":        func(i *S2SInvite) { i.RemoteSubnets = []string{"10.0.0.0/24\nPostUp = id"} },
		"expected":      func(i *S2SInvite) { i.ExpectedSubnets = []string{"nope"} },
		"no subnets":    func(i *S2SInvite) { i.RemoteSubnets = nil },
	} {
		inv := validInvite(t)
		mutate(inv)
		if _, err := ParseInviteToken(encodeTestInvite(t, inv)); !errors.Is(err, ErrInviteMalformed) {
			t.Errorf("%s: got %v, want ErrInviteMalformed", label, err)
		}
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
	// The tunnel subnet is not announced, but a peer still may not
	// claim it: the road-warrior peers live there.
	if _, ok := svc.subnetsConflict([]string{"10.10.11.0/24"}); !ok {
		t.Error("the WireGuard server subnet should be reported as conflict")
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

// newS2SPeerPair builds two routers that share nothing but the shipped
// defaults: separate config files and server keys, different LANs, and
// the same 10.10.11.1/24 tunnel subnet every LANKeeper starts with.
func newS2SPeerPair(t *testing.T) (a, b *VPNService) {
	t.Helper()
	a = newS2STestService(t)
	b = newS2STestService(t)
	b.cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan0", Device: "eth1", Role: "lan", Address: "192.168.5.1/24"},
	}
	return a, b
}

// TestS2SLinkRoutesOnlyTheFarLANs is the regression test for the
// addressing. The tunnel subnet was announced as a LAN, so two routers
// on the shipped 10.10.11.0/24 refused each other as overlapping, and a
// tunnel address taken from the originator's pool ended up in the
// joining side's AllowedIPs, where its own wgs0 never held it. Each side
// now routes exactly the other's LANs.
func TestS2SLinkRoutesOnlyTheFarLANs(t *testing.T) {
	useGenpskAgent(t)
	a, b := newS2SPeerPair(t)
	ctx := context.Background()

	tok, pending, err := a.CreateS2SInvite(ctx, "siteB", "", "203.0.113.5:51820", []string{"192.168.5.0/24"})
	if err != nil {
		t.Fatalf("CreateS2SInvite: %v", err)
	}
	_, _, joined, err := b.ConsumeInvite(ctx, tok)
	if err != nil {
		t.Fatalf("ConsumeInvite with both routers on the default tunnel subnet: %v", err)
	}
	if pending.AllowedIPs != "192.168.5.0/24" {
		t.Errorf("A routes %q to B, want only B's LAN", pending.AllowedIPs)
	}
	if joined.AllowedIPs != "10.10.10.0/24" {
		t.Errorf("B routes %q to A, want only A's LAN", joined.AllowedIPs)
	}
}

// TestConsumeRefusesAnInviteExpectingOtherLANs catches a typo on the
// originating side before it becomes a link that comes up and drops
// everything: A would accept traffic only from the LANs it expected.
func TestConsumeRefusesAnInviteExpectingOtherLANs(t *testing.T) {
	useGenpskAgent(t)
	a, b := newS2SPeerPair(t)
	ctx := context.Background()

	tok, _, err := a.CreateS2SInvite(ctx, "siteB", "", "203.0.113.5:51820", []string{"192.168.9.0/24"})
	if err != nil {
		t.Fatalf("CreateS2SInvite: %v", err)
	}
	if _, _, _, err := b.ConsumeInvite(ctx, tok); !errors.Is(err, ErrPeerSubnetMismatch) {
		t.Fatalf("got %v, want ErrPeerSubnetMismatch", err)
	}
	if n := len(b.cfg.VPN.Server.Peers); n != 0 {
		t.Errorf("a refused invite left %d peers behind", n)
	}
}

// s2sPingAgent records the ping argv and answers success.
type s2sPingAgent struct{ args []string }

func (a *s2sPingAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
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
	if method == "exec.run" && p.Cmd == "ping" {
		a.args = p.Args
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

// TestS2SReachabilityPingsFromTheLAN pins the probe's source. The far
// side's WireGuard accepts tunnel traffic only from this router's LANs,
// so a probe sourced from the wgs0 address was dropped there however
// healthy the link was.
func TestS2SReachabilityPingsFromTheLAN(t *testing.T) {
	agent := &s2sPingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := newS2STestService(t)
	svc.cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "siteB", PublicKey: testWGKey(t), AllowedIPs: "192.168.5.0/24", RemoteSubnets: []string{"192.168.5.0/24"}, IsSiteToSite: true},
	}
	if err := svc.S2SReachability(context.Background(), "siteB"); err != nil {
		t.Fatalf("S2SReachability: %v", err)
	}
	if got := strings.Join(agent.args, " "); got != "-c 1 -W 2 -I 10.10.10.1 192.168.5.1" {
		t.Errorf("ping %s, want the probe sent from the LAN address", got)
	}
}

// TestS2SHandshakeAcrossTwoRouters is the regression test. Both tokens
// were HMAC-signed with a key only the issuing router held, so the
// joining router could never verify an invite; and the ack carried the
// public half of a key pair generated and discarded on the spot, so
// even a verified exchange left the originator with a peer key that no
// private key matches. Now the originator records the joining router's
// own server key, the one its wgs0 runs with.
func TestS2SHandshakeAcrossTwoRouters(t *testing.T) {
	useGenpskAgent(t)
	a, b := newS2SPeerPair(t)
	ctx := context.Background()

	tok, pending, err := a.CreateS2SInvite(ctx, "siteB", "Istanbul", "203.0.113.5:51820", []string{"192.168.5.0/24"})
	if err != nil {
		t.Fatalf("CreateS2SInvite: %v", err)
	}
	if !pending.Pending || pending.PublicKey != "" {
		t.Errorf("a fresh invite must be pending with no key: %+v", pending)
	}

	ack := joinAndCheckKeys(t, a, b, tok, pending.PresharedKey)

	finalized, err := a.FinalizeInvite(ctx, "siteB", ack)
	if err != nil {
		t.Fatalf("FinalizeInvite: %v", err)
	}
	if finalized.Pending {
		t.Error("the finalized peer is still pending")
	}
	if finalized.PublicKey != b.cfg.VPN.Server.PublicKey {
		t.Errorf("A recorded B's key as %q, want B's server key %q", finalized.PublicKey, b.cfg.VPN.Server.PublicKey)
	}
}

// joinAndCheckKeys has B consume A's invite and checks both keys B
// recorded and handed back. It returns the ack token.
func joinAndCheckKeys(t *testing.T, a, b *VPNService, tok, psk string) string {
	t.Helper()
	ack, bPub, joined, err := b.ConsumeInvite(context.Background(), tok)
	if err != nil {
		t.Fatalf("ConsumeInvite: %v", err)
	}
	if bPub != b.cfg.VPN.Server.PublicKey {
		t.Errorf("B handed back %q, not its server key %q", bPub, b.cfg.VPN.Server.PublicKey)
	}
	if joined.PublicKey != a.cfg.VPN.Server.PublicKey || joined.PresharedKey != psk {
		t.Errorf("B recorded A with the wrong keys: %+v", joined)
	}
	return ack
}

// TestFinalizeRejectsAnAckMACedWithAnotherKey keeps the ack bound to
// its invite: only the holder of that invite's preshared key can make
// an ack the originator accepts.
func TestFinalizeRejectsAnAckMACedWithAnotherKey(t *testing.T) {
	useGenpskAgent(t)
	a := newS2STestService(t)
	ctx := context.Background()
	if _, _, err := a.CreateS2SInvite(ctx, "siteB", "", "203.0.113.5:51820", []string{"192.168.5.0/24"}); err != nil {
		t.Fatalf("CreateS2SInvite: %v", err)
	}
	forged, err := signAck(&S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "siteB", PublicKey: testWGKey(t)}, testWGKey(t))
	if err != nil {
		t.Fatalf("sign ack: %v", err)
	}
	if _, err := a.FinalizeInvite(ctx, "siteB", forged); !errors.Is(err, ErrInviteSignature) {
		t.Fatalf("got %v, want ErrInviteSignature", err)
	}
	if peer := a.findS2SPeer("siteB"); peer == nil || !peer.Pending || peer.PublicKey != "" {
		t.Errorf("a forged ack changed the pending peer: %+v", peer)
	}
}

// TestS2SNeedsAServerKeyPair refuses both ends of the wizard on a router
// whose WireGuard server was never set up, since the key handed to the
// other side would be empty.
func TestS2SNeedsAServerKeyPair(t *testing.T) {
	useGenpskAgent(t)
	a, b := newS2SPeerPair(t)
	ctx := context.Background()

	tok, _, err := a.CreateS2SInvite(ctx, "siteB", "", "203.0.113.5:51820", []string{"192.168.5.0/24"})
	if err != nil {
		t.Fatalf("CreateS2SInvite: %v", err)
	}
	b.cfg.VPN.Server.PublicKey = ""
	if _, _, _, err := b.ConsumeInvite(ctx, tok); !errors.Is(err, ErrS2SServerKeyMissing) {
		t.Errorf("join: got %v, want ErrS2SServerKeyMissing", err)
	}
	a.cfg.VPN.Server.PublicKey = ""
	if _, _, err := a.CreateS2SInvite(ctx, "siteC", "", "203.0.113.5:51820", []string{"192.168.6.0/24"}); !errors.Is(err, ErrS2SServerKeyMissing) {
		t.Errorf("invite: got %v, want ErrS2SServerKeyMissing", err)
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
