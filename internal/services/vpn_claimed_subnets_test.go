package services

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// WireGuard hands an allowed IP to the last peer that claims it, so a
// new peer may not overlap another peer, a VLAN or the running OpenVPN
// pool.
func TestSubnetsConflictCoversPeersVLANsAndOpenVPN(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.VLANs = []config.VLANConfig{{ID: "iot", Parent: "lan", VID: 20, Address: "10.10.20.1/24"}}
	cfg.OpenVPN.Server.Enabled = true
	cfg.OpenVPN.Server.Subnet = "10.8.0.0/24"
	cfg.VPN.Server.Peers = []config.WGServerPeer{{
		Name: "site-a", AllowedIPs: "10.10.11.2/32, 192.168.50.0/24",
		RemoteSubnets: []string{"192.168.50.0/24"}, IsSiteToSite: true,
	}}
	svc := NewVPNService(cfg)

	for _, remote := range []string{"192.168.50.0/24", "10.10.20.0/24", "10.8.0.0/24"} {
		if _, bad := svc.subnetsConflict([]string{remote}); !bad {
			t.Errorf("%s was accepted although something already routes it", remote)
		}
	}
	if conflict, bad := svc.subnetsConflict([]string{"192.168.60.0/24"}); bad {
		t.Errorf("a free subnet was refused over %s", conflict)
	}
}
