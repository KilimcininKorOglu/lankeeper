package services

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestDHCPRenderConfigAppliesDefaultsAndVLANRanges renders the shipped
// dnsmasq template and pins the defaults for unset fields and the range
// each DHCP-enabled VLAN receives. The template path is relative to the
// repository root, so the test runs from there.
func TestDHCPRenderConfigAppliesDefaultsAndVLANRanges(t *testing.T) {
	t.Chdir("../..")

	cfg := &config.Config{}
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "eth0", Role: "wan"},
		{ID: "lan", Device: "eth1", Role: "lan"},
	}
	cfg.DHCP.RangeStart = "10.10.10.100"
	cfg.DHCP.RangeEnd = "10.10.10.200"
	cfg.IPv6.Enabled = "off"
	cfg.VLANs = []config.VLANConfig{
		{Parent: "lan", VID: 20, Address: "10.10.20.1/24", DHCP: config.VLANDHCPConfig{Enabled: true}},
		{Parent: "lan", VID: 30, Address: "10.10.30.1/24"},
		{Parent: "missing", VID: 40, Address: "10.10.40.1/24", DHCP: config.VLANDHCPConfig{Enabled: true}},
	}

	out, err := NewDHCPService(cfg).RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, want := range []string{"interface=eth1", "10.10.10.100,10.10.10.200", "12h", "10.10.10.1", "lan", "eth1.20"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"eth1.30", ".40"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("rendered config carries %q:\n%s", unwanted, out)
		}
	}
}
