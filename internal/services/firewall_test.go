package services_test

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
)

const testNftTemplate = `flush ruleset
table inet filter {
    chain input {
        type filter hook input priority 0; policy drop;
        ct state established,related accept
{{- range .LANInterfaces }}
        iifname "{{ .Device }}" accept
{{- end }}
{{- if .IPv6Enabled }}
        ip6 nexthdr icmpv6 accept
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

func testFirewallConfig() *config.Config {
	cfg := &config.Config{}
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
	cfg := testFirewallConfig()
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
	cfg := testFirewallConfig()
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
	cfg := testFirewallConfig()
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
	cfg := testFirewallConfig()
	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)

	svc.AddPortForward(config.PortForward{
		Name:     "SSH",
		Protocol: "tcp",
		ExtPort:  2222,
		IntIP:    "10.10.10.50",
		IntPort:  22,
		Enabled:  true,
	})

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
	cfg := testFirewallConfig()
	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)

	if err := svc.RemovePortForward(5); err == nil {
		t.Error("should error on invalid index")
	}
}

func TestFirewallHasPendingChange(t *testing.T) {
	cfg := testFirewallConfig()
	svc, _ := services.NewFirewallServiceFromFS(cfg, testNftTemplate)

	if svc.HasPendingChange() {
		t.Error("should not have pending change initially")
	}
}
