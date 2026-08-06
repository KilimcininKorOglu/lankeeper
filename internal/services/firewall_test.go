package services_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

const testNftTemplate = `flush ruleset
table inet filter {
    chain input {
        type filter hook input priority 0; policy drop;
        ct state established,related accept
        ct state invalid drop
{{- range .CustomInputRules }}
{{ . }}
{{- end }}
{{- range .LANInterfaces }}
        iifname "{{ .Device }}" accept
{{- end }}
{{- if .IPv6Enabled }}
        ip6 nexthdr icmpv6 accept
{{- end }}
    }
    chain forward {
        type filter hook forward priority 0; policy drop;
        ct state invalid drop
{{- range .CustomForwardRules }}
{{ . }}
{{- end }}
    }
    chain output {
        type filter hook output priority 0; policy accept;
{{- range .CustomOutputRules }}
{{ . }}
{{- end }}
    }
}
table ip nat {
    chain postrouting {
        type nat hook postrouting priority 100; policy accept;
{{- range .WANInterfaces }}
        oifname "{{ .Device }}" masquerade
{{- end }}
{{- if .TTLFixEnabled }}
        ip ttl set {{ .TTLFixValue }}
{{- end }}
    }
}
`

func testFirewallConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
	}
	cfg.System.WebPort = 8443
	cfg.Firewall.DefaultPolicy = "drop"
	cfg.Firewall.RateLimits = map[string]string{
		"ssh": "3/minute",
		"web": "30/minute",
	}
	cfg.IPv6.Enabled = "auto"
	return cfg
}

func TestFirewallRenderConfig(t *testing.T) {
	cfg := testFirewallConfig(t)
	svc, err := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}

	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render config: %v", err)
	}

	if !strings.Contains(rendered, "flush ruleset") {
		t.Error("should contain flush ruleset")
	}
	if !strings.Contains(rendered, `iifname "enp0s25" accept`) {
		t.Error("should contain LAN accept rule")
	}
	if !strings.Contains(rendered, `oifname "enp3s0" masquerade`) {
		t.Error("should contain WAN masquerade")
	}
	if !strings.Contains(rendered, "icmpv6") {
		t.Error("should contain ICMPv6 rule when IPv6 enabled")
	}
}

func TestFirewallRenderWithTTLFix(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.Firewall.TTLFix.Enabled = true
	cfg.Firewall.TTLFix.Value = 64

	svc, err := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}

	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(rendered, "ip ttl set 64") {
		t.Error("should contain TTL fix rule")
	}
}

func TestFirewallRenderWithoutIPv6(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.IPv6.Enabled = "off"

	svc, err := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(rendered, "icmpv6") {
		t.Error("should NOT contain ICMPv6 when IPv6 disabled")
	}
}

func TestFirewallPortForwardCRUD(t *testing.T) {
	cfg := testFirewallConfig(t)
	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)

	if err := svc.AddPortForward(config.PortForward{
		Name:     "SSH",
		Protocol: "tcp",
		ExtPort:  2222,
		IntIP:    "10.10.10.50",
		IntPort:  22,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("add port forward: %v", err)
	}

	if len(cfg.Firewall.PortForwards) != 1 {
		t.Fatalf("expected 1 port forward, got %d", len(cfg.Firewall.PortForwards))
	}

	if err := svc.RemovePortForward(0); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if len(cfg.Firewall.PortForwards) != 0 {
		t.Error("port forwards should be empty after removal")
	}
}

func TestFirewallRemoveInvalidIndex(t *testing.T) {
	cfg := testFirewallConfig(t)
	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)

	if err := svc.RemovePortForward(5); err == nil {
		t.Error("should error on invalid index")
	}
}

func TestFirewallHasPendingChange(t *testing.T) {
	cfg := testFirewallConfig(t)
	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)

	if svc.HasPendingChange() {
		t.Error("should not have pending change initially")
	}
}

const testNftWGTemplate = `flush ruleset
table inet filter {
    chain forward {
        type filter hook forward priority 0; policy drop;
{{- range .LANInterfaces }}
{{- range $.WANInterfaces }}
        iifname "{{ $.LANDevice }}" oifname "{{ .Device }}" accept
{{- end }}
{{- end }}
{{- if .WGServerEnabled }}
{{- range .LANInterfaces }}
        iifname "{{ $.WGServerIface }}" oifname "{{ .Device }}" accept
        iifname "{{ .Device }}" oifname "{{ $.WGServerIface }}" accept
{{- end }}
{{- end }}
{{- range $wg := .WGClientIfaces }}
{{- range $.LANInterfaces }}
        iifname "{{ $wg }}" oifname "{{ .Device }}" accept
        iifname "{{ .Device }}" oifname "{{ $wg }}" accept
{{- end }}
{{- end }}
    }
}
`

func TestFirewallRenderWithWireGuard(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.VPN.Server.Enabled = true
	cfg.VPN.Clients = []config.WGClientTunnel{
		{Name: "nl-amsterdam", Table: 100, Fwmark: 100},
		{Name: "us-newyork", Table: 101, Fwmark: 101},
	}

	svc, err := services.NewFirewallServiceFromFS(cfg, testNftWGTemplate)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(rendered, `iifname "wgs0" oifname "enp0s25" accept`) {
		t.Errorf("should contain WG server → LAN rule, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `iifname "enp0s25" oifname "wgs0" accept`) {
		t.Errorf("should contain LAN → WG server rule, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `iifname "wg0" oifname "enp0s25" accept`) {
		t.Errorf("should contain WG client 0 → LAN rule, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `iifname "wg1" oifname "enp0s25" accept`) {
		t.Errorf("should contain WG client 1 → LAN rule, got:\n%s", rendered)
	}
}

func TestFirewallRenderWithoutWireGuard(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.VPN.Server.Enabled = false

	svc, err := services.NewFirewallServiceFromFS(cfg, testNftWGTemplate)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(rendered, "wgs0") {
		t.Error("should NOT contain WG server rules when disabled")
	}
	if strings.Contains(rendered, "wg0") {
		t.Error("should NOT contain WG client rules when no clients")
	}
}

const testNftOVPNTemplate = `flush ruleset
table inet filter {
    chain forward {
        type filter hook forward priority 0; policy drop;
{{- if .OVPNServerEnabled }}
{{- range .LANInterfaces }}
        iifname "{{ $.OVPNServerIface }}" oifname "{{ .Device }}" accept
        iifname "{{ .Device }}" oifname "{{ $.OVPNServerIface }}" accept
{{- end }}
{{- end }}
    }
}
`

func TestFirewallRenderWithOpenVPN(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.OpenVPN.Server.Enabled = true
	cfg.OpenVPN.Server.Device = "tun0"

	svc, err := services.NewFirewallServiceFromFS(cfg, testNftOVPNTemplate)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(rendered, `iifname "tun0" oifname "enp0s25" accept`) {
		t.Errorf("should contain OVPN → LAN rule, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `iifname "enp0s25" oifname "tun0" accept`) {
		t.Errorf("should contain LAN → OVPN rule, got:\n%s", rendered)
	}
}

// TestFirewallRendersCustomRules is the regression test for custom
// rules never reaching the ruleset: the generator existed but had no
// caller, so a rule shown as Enabled in the UI changed nothing.
func TestFirewallRendersCustomRules(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.Firewall.Rules = []config.FirewallRule{
		{
			Name:    "block printer",
			Chain:   "input",
			Action:  "drop",
			SrcIP:   "10.10.10.55",
			Enabled: true,
		},
		{
			Name:     "allow ssh from admin",
			Chain:    "forward",
			Action:   "accept",
			SrcIP:    "10.10.10.0/24",
			Protocol: "tcp",
			Port:     22,
			Enabled:  true,
		},
		{
			Name:     "block outbound smtp",
			Chain:    "output",
			Action:   "reject",
			Protocol: "tcp",
			Port:     25,
			Enabled:  true,
		},
	}

	svc, err := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, want := range []string{
		"ip saddr 10.10.10.55 drop # block printer",
		"ip saddr 10.10.10.0/24 tcp dport 22 accept # allow ssh from admin",
		"tcp dport 25 reject # block outbound smtp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered ruleset missing %q\n---\n%s", want, out)
		}
	}
}

// TestFirewallCustomRulesLandInTheRightChain proves the Chain field is
// honoured. The original generator ignored it entirely.
func TestFirewallCustomRulesLandInTheRightChain(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.Firewall.Rules = []config.FirewallRule{
		{Name: "fwd", Chain: "forward", Action: "drop", SrcIP: "10.0.0.1", Enabled: true},
	}

	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	inputChain := out[strings.Index(out, "chain input"):strings.Index(out, "chain forward")]
	if strings.Contains(inputChain, "# fwd") {
		t.Error("forward rule rendered into the input chain")
	}
	if !strings.Contains(out[strings.Index(out, "chain forward"):], "# fwd") {
		t.Errorf("forward rule missing from the forward chain\n---\n%s", out)
	}
}

// TestFirewallCustomRulePrecedesBuiltins pins the ordering decision. A
// drop rule placed after the built-in LAN accept would never match.
func TestFirewallCustomRulePrecedesBuiltins(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.Firewall.Rules = []config.FirewallRule{
		{Name: "blocked", Chain: "input", Action: "drop", SrcIP: "10.10.10.55", Enabled: true},
	}

	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	custom := strings.Index(out, "# blocked")
	builtin := strings.Index(out, `iifname "enp0s25" accept`)
	if custom < 0 || builtin < 0 {
		t.Fatalf("expected both rules in output\n---\n%s", out)
	}
	if custom > builtin {
		t.Error("custom drop rule renders after the built-in LAN accept, so it can never match")
	}
}

// TestFirewallSkipsDisabledCustomRule keeps the Enabled toggle honest.
func TestFirewallSkipsDisabledCustomRule(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.Firewall.Rules = []config.FirewallRule{
		{Name: "off", Chain: "input", Action: "drop", SrcIP: "10.10.10.55", Enabled: false},
	}

	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out, "# off") {
		t.Error("disabled rule rendered into the ruleset")
	}
}

// TestFirewallRejectsInjectionInCustomRule covers rules that bypass the
// HTTP handler: hand-edited YAML, a restored backup, or a config written
// by a release without handler validation. The renderer must refuse them
// on its own, because the nftables file has no escaping.
func TestFirewallRejectsInjectionInCustomRule(t *testing.T) {
	cases := []struct {
		name string
		rule config.FirewallRule
	}{
		{
			name: "newline in rule name",
			rule: config.FirewallRule{
				Name:    "x\n        ip saddr 0.0.0.0/0 accept",
				Chain:   "input",
				Action:  "drop",
				SrcIP:   "10.10.10.55",
				Enabled: true,
			},
		},
		{
			name: "quote break-out in interface",
			rule: config.FirewallRule{
				Name:      "iface",
				Chain:     "input",
				Action:    "drop",
				Interface: `eth0" accept #`,
				Direction: "in",
				Enabled:   true,
			},
		},
		{
			name: "bogus address",
			rule: config.FirewallRule{
				Name:    "addr",
				Chain:   "input",
				Action:  "drop",
				SrcIP:   "10.10.10.55 accept; drop",
				Enabled: true,
			},
		},
		{
			name: "bogus action",
			rule: config.FirewallRule{
				Name:    "act",
				Chain:   "input",
				Action:  "accept\n        ip saddr 0.0.0.0/0 accept",
				SrcIP:   "10.10.10.55",
				Enabled: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testFirewallConfig(t)
			cfg.Firewall.Rules = []config.FirewallRule{tc.rule}

			svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
			out, err := svc.RenderConfig()
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if strings.Contains(out, "ip saddr 0.0.0.0/0 accept") {
				t.Errorf("injected statement reached the ruleset\n---\n%s", out)
			}
			if strings.Contains(out, `eth0" accept`) {
				t.Errorf("interface broke out of its quoted match\n---\n%s", out)
			}
		})
	}
}

// TestFirewallSkipsUnconditionalCustomRule guards against a rule with no
// match conditions rendering as a bare accept or drop for the chain.
func TestFirewallSkipsUnconditionalCustomRule(t *testing.T) {
	cfg := testFirewallConfig(t)
	cfg.Firewall.Rules = []config.FirewallRule{
		{Name: "catch all", Chain: "input", Action: "drop", Enabled: true},
	}

	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out, "# catch all") {
		t.Errorf("rule with no conditions rendered as an unconditional statement\n---\n%s", out)
	}
}
