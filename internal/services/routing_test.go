package services_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
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

	if err := svc.AddPolicy(config.RoutingPolicy{Name: "test", Enabled: true, Tunnel: "wg0"}); err != nil {
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
		if err := svc.AddPolicy(config.RoutingPolicy{Name: name, Enabled: true, Tunnel: "wg0"}); err != nil {
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

// TestRoutingNftScriptIsNftSyntax keeps shell syntax out of the script
// handed to `nft -f`. The chain was flushed with a `2>/dev/null`
// suffix, which nft rejects as a syntax error, so every PBR apply
// failed before any rule loaded.
func TestRoutingNftScriptIsNftSyntax(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Clients = []config.WGClientTunnel{{Name: "nl", Table: 100, Fwmark: 100}}
	svc := services.NewRoutingService(cfg)
	if err := svc.AddPolicy(config.RoutingPolicy{Name: "p", Enabled: true, SrcIPs: []string{"10.10.10.5"}, Tunnel: "nl"}); err != nil {
		t.Fatalf("add policy: %v", err)
	}

	lines := strings.Split(svc.GenerateNftRules(), "\n")
	for _, bad := range []string{"2>", "/dev/null", "||", "&&"} {
		for _, l := range lines {
			if strings.Contains(l, bad) {
				t.Errorf("script line carries shell syntax %q: %s", bad, l)
			}
		}
	}
	// The chain must exist before it is flushed, or the flush fails on
	// the first apply.
	if !strings.HasPrefix(lines[0], "add chain inet filter pbr_policies ") ||
		lines[1] != "flush chain inet filter pbr_policies" {
		t.Errorf("script must add then flush the chain, got:\n%s", strings.Join(lines[:2], "\n"))
	}
}

// TestRoutingApplyLoadsTheScriptTheAgentWrote pins that nft loads the
// PBR script from a file the agent itself wrote. The web process wrote
// it to its own /tmp, which PrivateTmp separates from the agent's, so
// the agent's `nft -f` found no file.
func TestRoutingApplyLoadsTheScriptTheAgentWrote(t *testing.T) {
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Clients = []config.WGClientTunnel{{Name: "nl", Table: 100, Fwmark: 100}}
	svc := services.NewRoutingService(cfg)
	if err := svc.AddPolicy(config.RoutingPolicy{Name: "p", Enabled: true, SrcIPs: []string{"10.10.10.5"}, Tunnel: "nl"}); err != nil {
		t.Fatalf("add policy: %v", err)
	}
	if err := svc.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var loaded string
	for _, c := range agent.execCallsCopy() {
		if c.Cmd == "nft" && len(c.Args) == 2 && c.Args[0] == "-f" {
			loaded = c.Args[1]
		}
	}
	if loaded == "" {
		t.Fatal("nft -f was never run")
	}
	if !strings.HasPrefix(loaded, "/tmp/lankeeper-") {
		t.Errorf("nft loads %s, which the agent write whitelist does not cover", loaded)
	}
	if !agent.wroteFile(loaded) {
		t.Errorf("nft loads %s, but the agent never wrote it (writes: %+v)", loaded, agent.writeLog)
	}
}

// TestAddPolicyRefusesANameThatBreaksTheNftScript pins the validation
// that has to hold before policies are loaded. The name becomes an nft
// set name in the script the root agent runs, so a semicolon or a
// newline in it would add a statement of the caller's choosing.
func TestAddPolicyRefusesANameThatBreaksTheNftScript(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewRoutingService(cfg)
	for _, p := range []config.RoutingPolicy{
		{Name: "a; flush ruleset", Tunnel: "wg0"},
		{Name: "a\nadd rule", Tunnel: "wg0"},
		{Name: "ok", Tunnel: ""},
		{Name: "ok", Tunnel: "wg0", Domains: []string{"example.com; drop"}},
	} {
		if err := svc.AddPolicy(p); err == nil {
			t.Errorf("policy %+v was accepted", p)
		}
	}
	if len(cfg.Routing.Policies) != 0 {
		t.Errorf("a refused policy was stored: %+v", cfg.Routing.Policies)
	}
}
