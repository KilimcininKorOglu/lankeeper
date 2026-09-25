package services

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestPPPoEPeerDataFillsLinkDefaults pins the defaults pppd receives
// when the operator leaves the link parameters unset, and that set
// values pass through unchanged.
func TestPPPoEPeerDataFillsLinkDefaults(t *testing.T) {
	cfg := &config.Config{}
	cfg.PPPoE.Username = "user"
	svc := NewPPPoEService(cfg)

	got := svc.peerData("eth0")
	want := peerTemplateData{WANDevice: "eth0", Username: "user", MTU: 1492, MRU: 1492,
		LCPEchoInterval: 10, LCPEchoFailure: 3, Holdoff: 5}
	if got != want {
		t.Errorf("defaults = %+v, want %+v", got, want)
	}

	cfg.PPPoE.MTU, cfg.PPPoE.MRU, cfg.PPPoE.Holdoff, cfg.PPPoE.IPv6CP = 1480, 1480, 30, true
	got = svc.peerData("eth0")
	if got.MTU != 1480 || got.MRU != 1480 || got.Holdoff != 30 || !got.IPv6CP {
		t.Errorf("set values were not kept: %+v", got)
	}
}

// TestFirstPPPAddressesSkipsLinkLocal pins which ppp0 addresses the
// status page reports.
func TestFirstPPPAddressesSkipsLinkLocal(t *testing.T) {
	v4, v6 := firstPPPAddresses([]string{"fe80::1/64", "100.64.0.2/32", "2001:db8::2/64", "100.64.0.3/32"})
	if v4 != "100.64.0.2" || v6 != "2001:db8::2" {
		t.Errorf("got %q, %q", v4, v6)
	}
}
