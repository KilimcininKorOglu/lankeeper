package services

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// The shipped client template must render against the real struct;
// a field the struct lacks fails the whole render and no tunnel starts.
func TestWireGuardClientTemplateRendersAgainstTheConfigStruct(t *testing.T) {
	t.Chdir("../..")
	agent := &fileWriteAgent{files: map[string]string{}}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	client := &config.WGClientTunnel{
		Name: "nl", Endpoint: "vpn.example:51820", PrivateKey: "priv", PublicKey: "pub",
		AllowedIPs: "0.0.0.0/0", Table: 100, Fwmark: 100,
		Address: "10.64.0.2/32", MTU: 1420, PresharedKey: "psk", Keepalive: 25,
	}
	path := filepath.Join("/etc/wireguard", "wg0.conf")
	if err := NewVPNService(config.DefaultConfig()).renderClientConfig(client, path); err != nil {
		t.Fatalf("render: %v", err)
	}
	got := agent.files[path]
	for _, want := range []string{"Address = 10.64.0.2/32", "MTU = 1420", "FwMark = 100", "PresharedKey = psk", "PersistentKeepalive = 25"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config lacks %q:\n%s", want, got)
		}
	}
}

// The far side of an outbound tunnel is a third-party VPN; only the
// LAN may open connections into it.
func TestShippedRulesetAcceptsNoNewConnectionsFromAClientTunnel(t *testing.T) {
	t.Chdir("../..")
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
	}
	cfg.VPN.Clients = []config.WGClientTunnel{{Name: "nl", Table: 100, Fwmark: 100}}
	svc, err := NewFirewallService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.stopWatchdog)
	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(rendered, `iifname "wg0" oifname`) {
		t.Fatalf("ruleset accepts connections from the client tunnel into the LAN:\n%s", rendered)
	}
	if !strings.Contains(rendered, `iifname "enp0s25" oifname "wg0" accept`) {
		t.Fatalf("ruleset lacks the LAN -> client tunnel accept:\n%s", rendered)
	}
}

// accept ends evaluation, so the MSS clamp must come before every accept
// in the forward chain or an outbound SYN never reaches it.
func TestShippedRulesetClampsMSSBeforeAnyForwardAccept(t *testing.T) {
	t.Chdir("../..")
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
	}
	svc, err := NewFirewallService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.stopWatchdog)
	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	fwd := rendered[strings.Index(rendered, "chain forward {"):]
	clamp, accept := firstRuleLine(fwd, "maxseg size set rt mtu"), firstRuleLine(fwd, "accept")
	if clamp < 0 || accept < 0 || clamp > accept {
		t.Fatalf("MSS clamp is not ahead of the first forward accept:\n%s", fwd)
	}
}

// firstRuleLine returns the index of the first non-comment line of text
// that contains needle, or -1.
func firstRuleLine(text, needle string) int {
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

// Every LAN interface needs its own LAN -> WAN accept; the rule was
// written with the first LAN's device for all of them.
func TestShippedRulesetForwardsEveryLAN(t *testing.T) {
	t.Chdir("../..")
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
		{ID: "lan2", Device: "enp4s0", Role: "lan"},
	}
	svc, err := NewFirewallService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.stopWatchdog)
	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, lan := range []string{"enp0s25", "enp4s0"} {
		if !strings.Contains(rendered, `iifname "`+lan+`" oifname "enp3s0" accept`) {
			t.Errorf("no LAN -> WAN accept for %s:\n%s", lan, rendered)
		}
	}
}

// With USB NAT on, LAN traffic routed over the phone must pass the
// forward chain, whose policy is drop.
func TestShippedRulesetForwardsLANToTheUSBTether(t *testing.T) {
	t.Chdir("../..")
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
	}
	cfg.USBTether.Enabled = true
	cfg.USBTether.NAT = true
	svc, err := NewFirewallService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.stopWatchdog)
	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, `iifname "enp0s25" oifname "usb0" accept`) {
		t.Fatalf("no LAN -> USB tether accept:\n%s", rendered)
	}
}
