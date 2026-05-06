package services_test

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// nftSixInFourTmpl exercises the new IPv6WANInterfaces / SixInFour
// hooks without dragging the full production template into the test
// harness. Mirrors the relevant subset of nftables.conf.tmpl.
const nftSixInFourTmpl = `flush ruleset
table inet filter {
    chain input {
        type filter hook input priority 0; policy drop;
{{- if .SixInFourEnabled }}
        ip saddr {{ .SixInFourServer }} ip protocol 41 accept
{{- end }}
    }
    chain forward {
        type filter hook forward priority 0; policy drop;
{{- range .LANInterfaces }}
{{- range $.IPv6WANInterfaces }}
        iifname "{{ $.LANDevice }}" oifname "{{ .Device }}" accept
        iifname "{{ .Device }}" oifname "{{ $.LANDevice }}" accept
{{- end }}
{{- end }}
    }
}
table ip nat {
    chain postrouting {
        type nat hook postrouting priority 100; policy accept;
{{- range .WANInterfaces }}
        oifname "{{ .Device }}" masquerade
{{- end }}
        # IPv6WANInterfaces deliberately NOT masqueraded — no NAT66.
    }
}
`

func newFirewall6in4Config(t *testing.T) *config.Config {
	t.Helper()
	cfg := testFirewallConfig(t)
	cfg.IPv6.Mode = "6in4"
	cfg.IPv6.Enabled = "auto"
	cfg.IPv6.Tunnel = config.IPv6TunnelConfig{
		Provider:     "he.net",
		ServerIPv4:   "216.66.80.30",
		ClientIPv6:   "2001:470:1f0a:abc::2",
		RoutedPrefix: "2001:470:abcd::/48",
		TunnelID:     "1234567",
		Device:       "lkt6in4",
	}
	return cfg
}

func TestFirewallSixInFourAddsTunnelInterfaceAndProto41(t *testing.T) {
	cfg := newFirewall6in4Config(t)
	svc, err := services.NewFirewallServiceFromFS(cfg, nftSixInFourTmpl)
	if err != nil {
		t.Fatalf("NewFirewallServiceFromFS: %v", err)
	}

	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}

	if !strings.Contains(out, "ip saddr 216.66.80.30 ip protocol 41 accept") {
		t.Errorf("expected protocol-41 ingress rule, got:\n%s", out)
	}
	// LAN → tunnel forward (and reverse) must appear once each.
	if !strings.Contains(out, `iifname "enp0s25" oifname "lkt6in4" accept`) {
		t.Errorf("missing LAN → tunnel forward, got:\n%s", out)
	}
	if !strings.Contains(out, `iifname "lkt6in4" oifname "enp0s25" accept`) {
		t.Errorf("missing tunnel → LAN forward, got:\n%s", out)
	}
	// MASQUERADE block must NOT carry the tunnel device — no NAT66.
	if strings.Contains(out, `oifname "lkt6in4" masquerade`) {
		t.Errorf("tunnel device wrongly masqueraded:\n%s", out)
	}
}

func TestFirewallSixInFourSkippedWhenModeOther(t *testing.T) {
	cfg := newFirewall6in4Config(t)
	cfg.IPv6.Mode = "dhcpv6-pd" // back to PD
	svc, err := services.NewFirewallServiceFromFS(cfg, nftSixInFourTmpl)
	if err != nil {
		t.Fatalf("NewFirewallServiceFromFS: %v", err)
	}
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if strings.Contains(out, "protocol 41 accept") {
		t.Errorf("protocol-41 rule leaked into non-6in4 mode:\n%s", out)
	}
	if strings.Contains(out, "lkt6in4") {
		t.Errorf("tunnel device leaked into non-6in4 mode:\n%s", out)
	}
}

func TestFirewallSixInFourDisabledWhenIPv6Off(t *testing.T) {
	cfg := newFirewall6in4Config(t)
	cfg.IPv6.Enabled = "off"
	svc, err := services.NewFirewallServiceFromFS(cfg, nftSixInFourTmpl)
	if err != nil {
		t.Fatalf("NewFirewallServiceFromFS: %v", err)
	}
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if strings.Contains(out, "lkt6in4") {
		t.Errorf("tunnel forward leaked while IPv6 is off:\n%s", out)
	}
}

func TestFirewallSixInFourSkipsProto41WithoutServer(t *testing.T) {
	cfg := newFirewall6in4Config(t)
	cfg.IPv6.Tunnel.ServerIPv4 = "" // operator hasn't filled it yet
	svc, err := services.NewFirewallServiceFromFS(cfg, nftSixInFourTmpl)
	if err != nil {
		t.Fatalf("NewFirewallServiceFromFS: %v", err)
	}
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if strings.Contains(out, "protocol 41") {
		t.Errorf("protocol-41 rule emitted without ServerIPv4:\n%s", out)
	}
}
