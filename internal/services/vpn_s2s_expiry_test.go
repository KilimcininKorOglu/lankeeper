package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// pendingPeerWithExpiry seeds the originating side's state as it looks
// between issuing an invite and receiving the ack.
func pendingPeerWithExpiry(t *testing.T, expires time.Time) (*VPNService, string) {
	t.Helper()
	svc := newS2STestService(t)

	const peerName = "branch"
	svc.cfg.VPN.Server.Peers = []config.WGServerPeer{{
		Name:            peerName,
		AllowedIPs:      "10.10.11.2/32, 192.168.5.0/24",
		RemoteSubnets:   []string{"192.168.5.0/24"},
		IsSiteToSite:    true,
		Pending:         true,
		InviteExpiresAt: expires,
	}}
	return svc, peerName
}

// ackFor mints the token the joining side hands back.
func ackFor(t *testing.T, svc *VPNService, peerName string) string {
	t.Helper()
	token, err := svc.signToken(&S2SAck{
		Version:   inviteSchemaVersion,
		Kind:      tokenKindAck,
		Name:      peerName,
		PublicKey: "fakeJoiningSidePubKeyAABBCCDDEEFF1122334=",
	})
	if err != nil {
		t.Fatalf("sign ack: %v", err)
	}
	return token
}

// TestFinalizeRejectsAnExpiredInvite is the regression test. The
// originating side checked only the Pending flag, which the GC ticker
// clears on a five-minute cycle, so a leaked or delayed invite could
// still be converted into a permanent trusted peer for up to one sweep
// past its published deadline. The ack token carries no expiry of its
// own, so finalize is the only place this side can enforce the limit.
func TestFinalizeRejectsAnExpiredInvite(t *testing.T) {
	svc, peerName := pendingPeerWithExpiry(t, time.Now().Add(-time.Minute))
	ack := ackFor(t, svc, peerName)

	_, err := svc.FinalizeInvite(context.Background(), peerName, ack)
	if err == nil {
		t.Fatal("an expired invite was finalized")
	}
	if !errors.Is(err, ErrInviteExpired) {
		t.Errorf("got %v, want ErrInviteExpired", err)
	}

	// The peer must stay pending rather than become trusted.
	peer := svc.cfg.VPN.Server.Peers[0]
	if !peer.Pending {
		t.Error("the peer was promoted out of pending despite the rejection")
	}
	if peer.PublicKey != "" {
		t.Errorf("a public key was recorded for a rejected finalize: %q", peer.PublicKey)
	}
}

// TestFinalizeRejectsAnInviteThatExpiredMomentsAgo covers the boundary
// the GC interval used to paper over: expiry is enforced the instant it
// passes, not whenever the sweep next runs.
func TestFinalizeRejectsAnInviteThatExpiredMomentsAgo(t *testing.T) {
	svc, peerName := pendingPeerWithExpiry(t, time.Now().Add(-time.Millisecond))
	ack := ackFor(t, svc, peerName)

	if _, err := svc.FinalizeInvite(context.Background(), peerName, ack); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("got %v, want ErrInviteExpired", err)
	}
}

// TestFinalizeAcceptsAValidInvite keeps the check from breaking the
// path it guards.
func TestFinalizeAcceptsAValidInvite(t *testing.T) {
	svc, peerName := pendingPeerWithExpiry(t, time.Now().Add(30*time.Minute))
	ack := ackFor(t, svc, peerName)

	peer, err := svc.FinalizeInvite(context.Background(), peerName, ack)
	if err != nil {
		t.Fatalf("a live invite was refused: %v", err)
	}
	if peer.Pending {
		t.Error("the peer is still pending after a successful finalize")
	}
	if peer.PublicKey == "" {
		t.Error("no public key was recorded")
	}
	if !peer.InviteExpiresAt.IsZero() {
		t.Error("the expiry was not cleared on success")
	}
}

// TestFinalizeAcceptsAPeerWithNoRecordedExpiry keeps peers written by a
// release that predates the field working, since a zero time must not
// read as "expired in 1970".
func TestFinalizeAcceptsAPeerWithNoRecordedExpiry(t *testing.T) {
	svc, peerName := pendingPeerWithExpiry(t, time.Time{})
	ack := ackFor(t, svc, peerName)

	if _, err := svc.FinalizeInvite(context.Background(), peerName, ack); err != nil {
		t.Fatalf("a peer with no expiry was refused: %v", err)
	}
}

// TestGCStillReapsExpiredInvites keeps the sweep working; the finalize
// check makes enforcement deterministic but does not replace cleanup.
func TestGCStillReapsExpiredInvites(t *testing.T) {
	svc, _ := pendingPeerWithExpiry(t, time.Now().Add(-time.Hour))

	if n := svc.GCExpiredInvites(); n != 1 {
		t.Errorf("the collector reaped %d peers, want 1", n)
	}
	if len(svc.cfg.VPN.Server.Peers) != 0 {
		t.Error("the expired pending peer survived the sweep")
	}
}
