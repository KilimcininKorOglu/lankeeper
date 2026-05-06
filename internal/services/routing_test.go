package services_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func TestNewRoutingService(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewRoutingService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestRoutingAddRemovePolicy(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewRoutingService(cfg)

	if err := svc.AddPolicy(config.RoutingPolicy{
		Name:    "xbox-vpn",
		Enabled: true,
		SrcMACs: []string{"aa:bb:cc:dd:ee:ff"},
		Tunnel:  "nl-amsterdam",
	}); err != nil {
		t.Fatalf("add policy: %v", err)
	}

	policies := svc.GetPolicies()
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(policies))
	}

	if policies[0].Priority == 0 {
		t.Error("auto-priority should be non-zero")
	}

	if err := svc.RemovePolicy("xbox-vpn"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if len(svc.GetPolicies()) != 0 {
		t.Error("should be empty after removal")
	}
}

func TestRoutingTogglePolicy(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewRoutingService(cfg)

	if err := svc.AddPolicy(config.RoutingPolicy{Name: "test", Enabled: true}); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := svc.TogglePolicy("test", false); err != nil {
		t.Fatalf("toggle: %v", err)
	}

	policies := svc.GetPolicies()
	if policies[0].Enabled {
		t.Error("should be disabled after toggle")
	}
}

func TestRoutingUpdatePriorities(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewRoutingService(cfg)

	for _, name := range []string{"a", "b", "c"} {
		if err := svc.AddPolicy(config.RoutingPolicy{Name: name, Enabled: true}); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}

	if err := svc.UpdatePriorities([]string{"c", "a", "b"}); err != nil {
		t.Fatalf("update priorities: %v", err)
	}

	policies := svc.GetPolicies()
	if policies[0].Name != "c" {
		t.Errorf("first policy should be 'c', got %q", policies[0].Name)
	}
}

func TestRoutingGenerateNftRules(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Clients = []config.WGClientTunnel{
		{Name: "nl-amsterdam", Table: 100, Fwmark: 100},
	}

	svc := services.NewRoutingService(cfg)
	if err := svc.AddPolicy(config.RoutingPolicy{
		Name:    "xbox",
		Enabled: true,
		SrcMACs: []string{"aa:bb:cc:dd:ee:ff"},
		Tunnel:  "nl-amsterdam",
	}); err != nil {
		t.Fatalf("add policy: %v", err)
	}

	rules := svc.GenerateNftRules()
	if !strings.Contains(rules, "ether saddr aa:bb:cc:dd:ee:ff meta mark set 100") {
		t.Errorf("expected fwmark rule, got:\n%s", rules)
	}
	if !strings.Contains(rules, "pbr_policies") {
		t.Error("should contain pbr_policies chain")
	}
	if !strings.Contains(rules, "ct mark set meta mark") {
		t.Error("should contain ct mark preservation rule")
	}
}

func TestRoutingDstIPRules(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Clients = []config.WGClientTunnel{
		{Name: "vpn1", Table: 200, Fwmark: 200},
	}

	svc := services.NewRoutingService(cfg)
	if err := svc.AddPolicy(config.RoutingPolicy{
		Name:    "netflix",
		Enabled: true,
		DstIPs:  []string{"1.2.3.0/24", "4.5.6.0/24"},
		Tunnel:  "vpn1",
	}); err != nil {
		t.Fatalf("add policy: %v", err)
	}

	rules := svc.GenerateNftRules()
	if !strings.Contains(rules, "ip daddr 1.2.3.0/24 meta mark set 200") {
		t.Errorf("expected dst IP rule, got:\n%s", rules)
	}
}

func TestRoutingPortRules(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Clients = []config.WGClientTunnel{
		{Name: "vpn1", Table: 200, Fwmark: 200},
	}

	svc := services.NewRoutingService(cfg)
	if err := svc.AddPolicy(config.RoutingPolicy{
		Name:     "gaming",
		Enabled:  true,
		DstPorts: []int{3478, 3479},
		Protocol: "udp",
		Tunnel:   "vpn1",
	}); err != nil {
		t.Fatalf("add policy: %v", err)
	}

	rules := svc.GenerateNftRules()
	if !strings.Contains(rules, "udp dport 3478 meta mark set 200") {
		t.Errorf("expected port rule, got:\n%s", rules)
	}
}

func TestRoutingScheduleRules(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Clients = []config.WGClientTunnel{
		{Name: "vpn1", Table: 100, Fwmark: 100},
	}

	svc := services.NewRoutingService(cfg)
	if err := svc.AddPolicy(config.RoutingPolicy{
		Name:     "night-vpn",
		Enabled:  true,
		SrcIPs:   []string{"10.10.10.50"},
		Tunnel:   "vpn1",
		Schedule: "22:00-06:00",
	}); err != nil {
		t.Fatalf("add policy: %v", err)
	}

	rules := svc.GenerateNftRules()
	if !strings.Contains(rules, `meta hour >= "22:00"`) {
		t.Errorf("expected schedule rule, got:\n%s", rules)
	}
	if !strings.Contains(rules, `meta hour < "06:00"`) {
		t.Errorf("expected schedule end, got:\n%s", rules)
	}
}

func TestRoutingKillSwitch(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Clients = []config.WGClientTunnel{
		{Name: "vpn1", Table: 100, Fwmark: 100},
	}

	svc := services.NewRoutingService(cfg)
	if err := svc.AddPolicy(config.RoutingPolicy{
		Name:       "secure",
		Enabled:    true,
		SrcMACs:    []string{"aa:bb:cc:dd:ee:ff"},
		Tunnel:     "vpn1",
		KillSwitch: true,
	}); err != nil {
		t.Fatalf("add policy: %v", err)
	}

	rules := svc.GenerateNftRules()
	if !strings.Contains(rules, "meta mark != 100 drop") {
		t.Errorf("expected kill switch drop rule, got:\n%s", rules)
	}
}

func TestRoutingDomainSet(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Clients = []config.WGClientTunnel{
		{Name: "vpn1", Table: 100, Fwmark: 100},
	}

	svc := services.NewRoutingService(cfg)
	if err := svc.AddPolicy(config.RoutingPolicy{
		Name:    "streaming",
		Enabled: true,
		Domains: []string{"netflix.com", "youtube.com"},
		Tunnel:  "vpn1",
	}); err != nil {
		t.Fatalf("add policy: %v", err)
	}

	rules := svc.GenerateNftRules()
	if !strings.Contains(rules, "pbr_streaming") {
		t.Errorf("expected domain set name, got:\n%s", rules)
	}
	if !strings.Contains(rules, "type ipv4_addr") {
		t.Errorf("expected set definition, got:\n%s", rules)
	}
	if !strings.Contains(rules, "@pbr_streaming") {
		t.Errorf("expected set reference in rule, got:\n%s", rules)
	}
}

func TestRoutingRemovePolicyNotFound(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewRoutingService(cfg)

	if err := svc.RemovePolicy("nonexistent"); err == nil {
		t.Error("should error for nonexistent policy")
	}
}
