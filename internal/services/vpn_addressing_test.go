package services

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestAddressToSubnetUsesTheMask is the regression test for the subnet
// helper, which zeroed only the last octet. Any mask other than /24
// produced an address that is not the network, so a LAN on a /16
// was announced as 10.20.30.0/16 and a /25 as the wrong half.
func TestAddressToSubnetUsesTheMask(t *testing.T) {
	svc := NewVPNService(config.DefaultConfig())
	for in, want := range map[string]string{
		"10.20.30.40/16":   "10.20.0.0/16",
		"192.168.1.130/25": "192.168.1.128/25",
		"10.10.10.1/24":    "10.10.10.0/24",
		"10.10.10.1":       "10.10.10.0/24",
		"fd00:1::1/64":     "fd00:1::/64",
	} {
		if got := svc.addressToSubnet(in); got != want {
			t.Errorf("addressToSubnet(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNextTunnelIPFollowsTheServerSubnet is the regression test for the
// allocator, which handed out 10.10.11.x whatever address the server
// had. On any other server subnet the peer got an address its own
// server could not route.
func TestNextTunnelIPFollowsTheServerSubnet(t *testing.T) {
	svc := NewVPNService(config.DefaultConfig())
	svc.cfg.VPN.Server.Address = "172.16.0.1/16"
	svc.cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "a", AllowedIPs: "172.16.0.2/32"},
	}
	got, err := svc.nextTunnelIP()
	if err != nil || got != "172.16.0.3/32" {
		t.Errorf("nextTunnelIP() = %q, %v; want 172.16.0.3/32", got, err)
	}
}

// TestNextTunnelIPSkipsNetworkServerAndBroadcast fills a /29 and checks
// the allocator never returns the network, the server or the broadcast
// address, and reports the pool as full afterwards.
func TestNextTunnelIPSkipsNetworkServerAndBroadcast(t *testing.T) {
	svc := NewVPNService(config.DefaultConfig())
	svc.cfg.VPN.Server.Address = "10.9.0.3/29"
	svc.cfg.VPN.Server.Peers = nil

	var got []string
	for {
		ip, err := svc.nextTunnelIP()
		if err != nil {
			break
		}
		got = append(got, ip)
		svc.cfg.VPN.Server.Peers = append(svc.cfg.VPN.Server.Peers, config.WGServerPeer{AllowedIPs: ip})
	}
	want := []string{"10.9.0.1/32", "10.9.0.2/32", "10.9.0.4/32", "10.9.0.5/32", "10.9.0.6/32"}
	if len(got) != len(want) {
		t.Fatalf("allocated %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allocation %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// TestNextTunnelIPRefusesAnUnparsableServerAddress keeps a broken
// server address from turning into an allocation.
func TestNextTunnelIPRefusesAnUnparsableServerAddress(t *testing.T) {
	svc := NewVPNService(config.DefaultConfig())
	svc.cfg.VPN.Server.Address = "not-an-address"
	if ip, err := svc.nextTunnelIP(); err == nil {
		t.Errorf("nextTunnelIP() = %q with an unparsable server address", ip)
	}
}
