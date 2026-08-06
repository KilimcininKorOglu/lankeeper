package services_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// openPortsConfig is the shared fixture: one WAN, one LAN, drop policy.
func openPortsConfig(t *testing.T, ports ...config.OpenPort) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
	}
	cfg.System.WebPort = 8443
	cfg.Firewall.DefaultPolicy = "drop"
	cfg.Firewall.OpenPorts = ports
	return cfg
}

func renderOpenPorts(t *testing.T, cfg *config.Config) string {
	t.Helper()
	svc, err := services.NewFirewallServiceFromFS(cfg, shippedNftTemplate(t))
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return out
}

// inputChain returns only the input chain, so a match cannot be
// satisfied by an unrelated rule in forward or nat.
func inputChain(t *testing.T, rendered string) string {
	t.Helper()
	const start = "chain input {"
	i := strings.Index(rendered, start)
	if i < 0 {
		t.Fatal("input chain not found in rendered ruleset")
	}
	rest := rendered[i:]
	j := strings.Index(rest, "chain forward {")
	if j < 0 {
		t.Fatal("forward chain not found, cannot bound the input chain")
	}
	return rest[:j]
}

// TestOpenPortsReachTheInputChain is the regression test. The CRUD, the
// routes and the UI badge all existed, but nothing carried the config
// slice into the template data and the template had no block for it, so
// an enabled entry changed nothing in the ruleset.
func TestOpenPortsReachTheInputChain(t *testing.T) {
	cfg := openPortsConfig(t, config.OpenPort{
		Name: "minecraft", Protocol: "tcp", Port: 25565, Enabled: true,
	})

	chain := inputChain(t, renderOpenPorts(t, cfg))
	want := "tcp dport 25565 ct state new accept # minecraft"
	if !strings.Contains(chain, want) {
		t.Errorf("input chain is missing %q:\n%s", want, chain)
	}
}

// TestOpenPortsRespectTheEnabledFlag keeps a disabled entry out of the
// ruleset, which is what the UI toggle promises.
func TestOpenPortsRespectTheEnabledFlag(t *testing.T) {
	cfg := openPortsConfig(t,
		config.OpenPort{Name: "on", Protocol: "tcp", Port: 8080, Enabled: true},
		config.OpenPort{Name: "off", Protocol: "tcp", Port: 9090, Enabled: false},
	)

	chain := inputChain(t, renderOpenPorts(t, cfg))
	if !strings.Contains(chain, "tcp dport 8080") {
		t.Error("enabled entry did not render")
	}
	if strings.Contains(chain, "9090") {
		t.Error("disabled entry rendered anyway")
	}
}

// TestOpenPortBothProtocolsRendersOnePerProtocol covers the "both"
// option the handler accepts, which has no single nftables spelling.
func TestOpenPortBothProtocolsRendersOnePerProtocol(t *testing.T) {
	cfg := openPortsConfig(t, config.OpenPort{
		Name: "dns", Protocol: "both", Port: 53, Enabled: true,
	})

	chain := inputChain(t, renderOpenPorts(t, cfg))
	for _, want := range []string{"tcp dport 53 ct state new accept", "udp dport 53 ct state new accept"} {
		if !strings.Contains(chain, want) {
			t.Errorf("missing %q:\n%s", want, chain)
		}
	}
}

// TestOpenPortSourceScopesTheRule checks the optional source, including
// the IPv6 case: the table is `inet`, so both families are valid there,
// but `ip saddr` against an IPv6 address is a syntax error that would
// take the whole ruleset down.
func TestOpenPortSourceScopesTheRule(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"v4 cidr", "192.168.1.0/24", "ip saddr 192.168.1.0/24 tcp dport 22 ct state new accept"},
		{"v4 host", "10.10.10.5", "ip saddr 10.10.10.5 tcp dport 22 ct state new accept"},
		{"v6 cidr", "2001:db8::/32", "ip6 saddr 2001:db8::/32 tcp dport 22 ct state new accept"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := openPortsConfig(t, config.OpenPort{
				Protocol: "tcp", Port: 22, Source: tc.source, Enabled: true,
			})
			chain := inputChain(t, renderOpenPorts(t, cfg))
			if !strings.Contains(chain, tc.want) {
				t.Errorf("missing %q:\n%s", tc.want, chain)
			}
		})
	}
}

// TestInvalidOpenPortIsSkippedNotFatal mirrors the custom-rule
// behaviour: a bad entry hand-edited into router.yaml is dropped with a
// log line instead of aborting the whole ruleset.
func TestInvalidOpenPortIsSkippedNotFatal(t *testing.T) {
	cfg := openPortsConfig(t,
		config.OpenPort{Name: "bad proto", Protocol: "sctp", Port: 9, Enabled: true},
		config.OpenPort{Name: "bad port", Protocol: "tcp", Port: 70000, Enabled: true},
		config.OpenPort{Name: "bad source", Protocol: "tcp", Port: 80, Source: "not-an-ip", Enabled: true},
		config.OpenPort{Name: "good", Protocol: "tcp", Port: 8080, Enabled: true},
	)

	chain := inputChain(t, renderOpenPorts(t, cfg))
	if !strings.Contains(chain, "tcp dport 8080 ct state new accept # good") {
		t.Errorf("the valid entry was lost:\n%s", chain)
	}
	for _, unwanted := range []string{"sctp", "70000", "not-an-ip"} {
		if strings.Contains(chain, unwanted) {
			t.Errorf("invalid entry %q leaked into the ruleset:\n%s", unwanted, chain)
		}
	}
}

// TestOpenPortsFollowCustomRules pins the ordering. Custom rules run
// first so an explicit drop wins over an opened port.
func TestOpenPortsFollowCustomRules(t *testing.T) {
	cfg := openPortsConfig(t, config.OpenPort{
		Name: "web", Protocol: "tcp", Port: 8080, Enabled: true,
	})
	cfg.Firewall.Rules = []config.FirewallRule{{
		Name: "block-scanner", Chain: "input", Action: "drop",
		SrcIP: "203.0.113.7", Enabled: true,
	}}

	chain := inputChain(t, renderOpenPorts(t, cfg))
	custom := strings.Index(chain, "ip saddr 203.0.113.7 drop")
	open := strings.Index(chain, "tcp dport 8080")
	if custom < 0 || open < 0 {
		t.Fatalf("expected both rules to render:\n%s", chain)
	}
	if custom > open {
		t.Errorf("custom rule must precede the open port:\n%s", chain)
	}
}
