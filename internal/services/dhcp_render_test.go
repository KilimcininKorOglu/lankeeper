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

// TestVLANDHCPRangeHasAddresses is the regression test. The VLAN range
// was rendered without its start and end, as dhcp-range=,,12h, which
// dnsmasq refuses as a bad dhcp-range, and a config it refuses takes
// the LAN's DHCP down with it. The range now comes from the entry, or
// from the VLAN subnet when the entry names none.
func TestVLANDHCPRangeHasAddresses(t *testing.T) {
	t.Chdir("../..")

	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{{ID: "lan", Device: "eth1", Role: "lan"}}
	cfg.IPv6.Enabled = "off"
	cfg.VLANs = []config.VLANConfig{
		{Parent: "lan", VID: 20, Address: "10.10.20.1/24", DHCP: config.VLANDHCPConfig{Enabled: true}},
		{Parent: "lan", VID: 21, Address: "10.10.21.1/24", DHCP: config.VLANDHCPConfig{
			Enabled: true, RangeStart: "10.10.21.50", RangeEnd: "10.10.21.60", LeaseTime: "1h"}},
		{Parent: "lan", VID: 22, Address: "10.10.22.1/28", DHCP: config.VLANDHCPConfig{Enabled: true}},
	}

	out, err := NewDHCPService(cfg).RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"dhcp-range=10.10.20.100,10.10.20.200,12h",
		"dhcp-range=10.10.21.50,10.10.21.60,1h",
		"dhcp-range=10.10.22.2,10.10.22.14,12h",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dhcp-range=,") {
		t.Errorf("a VLAN range has no addresses:\n%s", out)
	}
}

// TestDHCPRenderShippedIPv6DefaultsHasNoRALines pins that the main
// dnsmasq.conf carries no IPv6 RA lines. dnsmasq refuses the whole file
// on one bad line, and under the shipped IPv6 defaults the main template
// rendered an empty DNS address, an extra ra-param field and a range with
// no start address, which kept LAN DHCP from starting. The IPv6 service
// owns RA through its /etc/dnsmasq.d drop-in.
func TestDHCPRenderShippedIPv6DefaultsHasNoRALines(t *testing.T) {
	t.Chdir("../..")

	cfg := &config.Config{}
	cfg.Interfaces = []config.InterfaceConfig{{ID: "lan", Device: "br0", Role: "lan"}}
	cfg.DHCP.RangeStart = "10.10.10.100"
	cfg.DHCP.RangeEnd = "10.10.10.200"
	cfg.IPv6.Enabled = "auto"
	cfg.IPv6.LAN.ULA.Enabled = true
	cfg.IPv6.LAN.ULA.Prefix = "fd00:1234:5678::/48"

	out, err := NewDHCPService(cfg).RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, bad := range []string{"enable-ra", "ra-param", "option6:", "[]", "ra-only"} {
		if strings.Contains(out, bad) {
			t.Errorf("rendered dnsmasq.conf contains %q:\n%s", bad, out)
		}
	}
}

// TestVLANClientsAreToldTheRouterAddress is the regression test for the
// gateway. Clients on a VLAN were told the network address, 10.10.20.0,
// as their router and DNS server, which nothing answers on, so a lease
// came with no working route and no name resolution.
func TestVLANClientsAreToldTheRouterAddress(t *testing.T) {
	t.Chdir("../..")

	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{{ID: "lan", Device: "eth1", Role: "lan"}}
	cfg.IPv6.Enabled = "off"
	cfg.VLANs = []config.VLANConfig{
		{Parent: "lan", VID: 20, Address: "10.10.20.1/24", DHCP: config.VLANDHCPConfig{Enabled: true}},
	}

	out, err := NewDHCPService(cfg).RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"dhcp-option=eth1.20,option:router,10.10.20.1\n",
		"dhcp-option=eth1.20,option:dns-server,10.10.20.1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config lacks %q:\n%s", strings.TrimSpace(want), out)
		}
	}
}

// TestAddStaticLeaseRefusesAHostnameThatBreaksTheConfig is the regression
// test. The hostname reached dnsmasq.conf and unbound.conf unchecked, so a
// newline added a directive a root daemon runs, and a space made dnsmasq
// refuse the file.
func TestAddStaticLeaseRefusesAHostnameThatBreaksTheConfig(t *testing.T) {
	cfg := &config.Config{}
	svc := NewDHCPService(cfg)
	for _, name := range []string{"tv\ndhcp-script=/tmp/x", "Living Room TV", `a"b`, "-lead"} {
		if err := svc.AddStaticLease("aa:bb:cc:dd:ee:ff", "10.10.10.50", name); err == nil {
			t.Errorf("hostname %q was accepted", name)
		}
	}
	if len(cfg.DHCP.StaticLeases) != 0 {
		t.Errorf("a refused lease was stored: %+v", cfg.DHCP.StaticLeases)
	}
}
