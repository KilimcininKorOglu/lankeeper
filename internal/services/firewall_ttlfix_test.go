package services_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func TestSetTTLFixRejectsOutOfRangeValues(t *testing.T) {
	// nftables parses the ruleset as a unit, so a hop limit the kernel
	// will not accept does not fail its own line: it fails the whole
	// load and takes every unrelated rule down with it. The guard
	// therefore has to run before the value is stored, not when the
	// ruleset is rendered.
	for _, value := range []int{0, -1, 256, 1 << 20} {
		cfg := testFirewallConfig(t)
		cfg.Firewall.TTLFix.Value = 64

		svc, err := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
		if err != nil {
			t.Fatalf("new firewall service: %v", err)
		}

		err = svc.SetTTLFix(true, value)
		if err == nil {
			t.Fatalf("SetTTLFix(%d) was accepted, so an unloadable ruleset can be persisted", value)
		}
		if !errors.Is(err, services.ErrInvalidTTL) {
			t.Errorf("SetTTLFix(%d) error = %v, want ErrInvalidTTL so the handler can answer 400", value, err)
		}
		if cfg.Firewall.TTLFix.Enabled || cfg.Firewall.TTLFix.Value != 64 {
			t.Errorf("a rejected value still reached the config: %+v", cfg.Firewall.TTLFix)
		}
	}
}

func TestSetTTLFixAcceptsTheLegalRange(t *testing.T) {
	cfg := testFirewallConfig(t)
	svc, err := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}

	// 1 and 255 are both legal hop limits; an off-by-one in the bound
	// would reject a setting the operator is entitled to choose.
	for _, value := range []int{1, 64, 255} {
		if err := svc.SetTTLFix(true, value); err != nil {
			t.Fatalf("SetTTLFix(%d) was rejected, but it is a valid hop limit: %v", value, err)
		}
		if !cfg.Firewall.TTLFix.Enabled || cfg.Firewall.TTLFix.Value != value {
			t.Fatalf("config = %+v, want enabled with value %d", cfg.Firewall.TTLFix, value)
		}
	}
}

// Disabling has to remove the rule, not render it with a zero. A zero
// hop limit is rejected by nftables, so the difference decides whether
// the whole ruleset still loads.
func TestSetTTLFixDisableRemovesTheRule(t *testing.T) {
	cfg := testFirewallConfig(t)
	svc, err := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}

	if err := svc.SetTTLFix(true, 65); err != nil {
		t.Fatalf("enable: %v", err)
	}
	rendered, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render with ttl fix on: %v", err)
	}
	if !strings.Contains(rendered, "ip ttl set 65") {
		t.Errorf("ruleset does not carry the stored hop limit:\n%s", rendered)
	}

	if err := svc.SetTTLFix(false, 65); err != nil {
		t.Fatalf("disable: %v", err)
	}
	rendered, err = svc.RenderConfig()
	if err != nil {
		t.Fatalf("render with ttl fix off: %v", err)
	}
	if strings.Contains(rendered, "ip ttl set") {
		t.Errorf("disabling left the rule in the ruleset:\n%s", rendered)
	}
}
