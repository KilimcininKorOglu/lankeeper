package services_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// shippedNftTemplate loads the template the binary actually renders.
//
// Every other firewall test uses an inline copy, which cannot catch a
// defect that exists only in the shipped file. Tests run with
// CWD=internal/services, hence the relative path.
func shippedNftTemplate(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "configs", "sysconf", "nftables.conf.tmpl"))
	if err != nil {
		t.Fatalf("read shipped template: %v", err)
	}
	return string(b)
}

// TestShippedTemplateBindsTheIsolatedVLANDevice is the regression test.
// The isolation rules referenced `$.VLANDevice`, and `$` is the root
// object regardless of range nesting, so it never meant the current
// VLAN. The field it pointed at was declared but never assigned, so
// both rules rendered with an empty interface name that no device can
// match and the Isolated control enforced nothing.
func TestShippedTemplateBindsTheIsolatedVLANDevice(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
	}
	cfg.VLANs = []config.VLANConfig{
		{ID: "guest", Parent: "lan", VID: 30, Isolated: true},
		{ID: "office", Parent: "lan", VID: 40, Isolated: false},
	}
	cfg.System.WebPort = 8443
	cfg.Firewall.DefaultPolicy = "drop"

	svc, err := services.NewFirewallServiceFromFS(cfg, shippedNftTemplate(t))
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}
	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// An empty interface name is the exact symptom: a quoted empty
	// string that nft will accept and nothing will ever match.
	if strings.Contains(rendered, `iifname "" `) || strings.Contains(rendered, `oifname "" `) {
		t.Errorf("rendered an empty interface name:\n%s", vlanBlock(rendered))
	}

	for _, want := range []string{
		`iifname "enp0s25.30" oifname "enp0s25" drop`,
		`iifname "enp0s25" oifname "enp0s25.30" drop`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing isolation rule %q; block was:\n%s", want, vlanBlock(rendered))
		}
	}

	// The VLAN that is not isolated must not gain a drop rule.
	if strings.Contains(rendered, "enp0s25.40") {
		t.Error("a VLAN that is not isolated appeared in the isolation rules")
	}
}

// TestShippedTemplateOmitsIsolationWhenNoVLANIsIsolated keeps the block
// from emitting a stray rule on the common configuration.
func TestShippedTemplateOmitsIsolationWhenNoVLANIsIsolated(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
	}
	cfg.VLANs = []config.VLANConfig{{ID: "office", Parent: "lan", VID: 40}}
	cfg.System.WebPort = 8443
	cfg.Firewall.DefaultPolicy = "drop"

	svc, err := services.NewFirewallServiceFromFS(cfg, shippedNftTemplate(t))
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}
	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(rendered, "enp0s25.40") {
		t.Error("a VLAN that is not isolated produced isolation rules")
	}
}

// vlanBlock extracts the isolation section for a readable failure.
func vlanBlock(rendered string) string {
	const marker = "# Inter-VLAN isolation"
	i := strings.Index(rendered, marker)
	if i < 0 {
		return "(isolation block not found in output)"
	}
	rest := rendered[i:]
	lines := strings.SplitN(rest, "\n", 8)
	return strings.Join(lines[:min(len(lines), 7)], "\n")
}
