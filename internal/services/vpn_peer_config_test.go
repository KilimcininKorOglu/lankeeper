package services_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// The point of storing the key: the config the operator downloads at
// creation can be produced again later, byte for byte. A peer whose
// file was lost used to mean deleting and recreating it.
func TestPeerConfigMatchesTheOneIssuedAtCreation(t *testing.T) {
	svc, _ := newAllocTestVPN(t)

	peer, privKey, err := svc.AddPeer(context.Background(), "phone", false, nil, "")
	if err != nil {
		t.Fatalf("add peer: %v", err)
	}
	atCreation := svc.GeneratePeerConfig(peer, privKey)

	reissued, err := svc.PeerConfig("phone")
	if err != nil {
		t.Fatalf("re-issue: %v", err)
	}
	if reissued != atCreation {
		t.Errorf("re-issued config differs from the one downloaded at creation\nfirst:\n%s\nagain:\n%s",
			atCreation, reissued)
	}
	if !strings.Contains(reissued, privKey) {
		t.Error("the re-issued config does not carry the peer's private key, so it will not connect")
	}
}

// A peer written before the key was stored keeps working, because the
// server only needs its public key. Only re-issuing is lost, and that
// has to be reported as its own condition: the operator can act on it
// by replacing the peer, which a generic failure would not tell them.
func TestPeerConfigReportsAMissingKeySeparately(t *testing.T) {
	svc, cfg := newAllocTestVPN(t)
	cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "legacy", PublicKey: "PUB", AllowedIPs: "10.10.11.2/32"},
	}

	_, err := svc.PeerConfig("legacy")
	if !errors.Is(err, services.ErrPeerKeyUnavailable) {
		t.Fatalf("error = %v, want ErrPeerKeyUnavailable", err)
	}
	if svc.CanReissuePeer("legacy") {
		t.Error("CanReissuePeer said yes, so the page would offer a download that fails")
	}
}

func TestPeerConfigDistinguishesAnUnknownPeer(t *testing.T) {
	svc, _ := newAllocTestVPN(t)

	_, err := svc.PeerConfig("nobody")
	if !errors.Is(err, services.ErrPeerNotFound) {
		t.Fatalf("error = %v, want ErrPeerNotFound so the handler can answer 404", err)
	}
	if svc.CanReissuePeer("nobody") {
		t.Error("CanReissuePeer said yes for a peer that does not exist")
	}
}

func TestCanReissuePeerFollowsStoredState(t *testing.T) {
	svc, _ := newAllocTestVPN(t)

	if _, _, err := svc.AddPeer(context.Background(), "laptop", false, nil, ""); err != nil {
		t.Fatalf("add peer: %v", err)
	}
	if !svc.CanReissuePeer("laptop") {
		t.Error("a freshly added peer cannot be re-issued, so the key was not stored")
	}
}
