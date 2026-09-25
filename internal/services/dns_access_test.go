package services

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestUnboundAllowsEveryClientSubnet is the regression test. unbound
// refuses every query not covered by an access-control line, and the
// only ones rendered were 10.10.10.0/24 and, per VLAN, its parent's
// network with /24 appended. Clients on a VLAN, on a LAN with another
// address, and on the WireGuard and OpenVPN servers, which are all
// handed this router as their resolver, were refused.
func TestUnboundAllowsEveryClientSubnet(t *testing.T) {
	t.Chdir("../..")

	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "eth0", Role: "wan", Address: "203.0.113.2/24"},
		{ID: "lan", Device: "eth1", Role: "lan", Address: "192.168.1.1/24"},
	}
	cfg.VLANs = []config.VLANConfig{{ID: "guest", Parent: "lan", VID: 20, Address: "10.10.20.1/24"}}
	cfg.VPN.Server.Enabled = true
	cfg.VPN.Server.Address = "10.10.11.1/24"
	cfg.OpenVPN.Server.Enabled = true
	cfg.OpenVPN.Server.Subnet = "10.10.12.0/24"

	out, err := NewDNSService(cfg).RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, subnet := range []string{"192.168.1.0/24", "10.10.20.0/24", "10.10.11.0/24", "10.10.12.0/24"} {
		if !strings.Contains(out, "access-control: "+subnet+" allow") {
			t.Errorf("unbound does not allow %s:\n%s", subnet, out)
		}
	}
	if strings.Contains(out, "access-control: 203.0.113.0/24 allow") {
		t.Error("the WAN subnet is allowed to query the resolver")
	}
}
