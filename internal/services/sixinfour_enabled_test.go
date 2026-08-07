package services_test

import (
	"context"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// applyWithEnabled runs the tunnel's converge step for one combination
// of the two settings that select it, and returns the commands it
// issued.
func applyWithEnabled(t *testing.T, enabled, mode string) []string {
	t.Helper()
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := newSixInFourTestConfig(t)
	cfg.IPv6.Enabled = enabled
	cfg.IPv6.Mode = mode

	svc := services.NewSixInFourService(cfg)
	svc.SetStatePathForTest(t.TempDir() + "/tunnel.json")
	svc.SetLocalIPv4ForTest("198.51.100.7")

	if err := svc.ApplyConfig(context.Background()); err != nil {
		t.Fatalf("ApplyConfig(enabled=%q, mode=%q): %v", enabled, mode, err)
	}
	return flatten(agent.execCallsCopy())
}

// flatten renders the recorded calls as one string per command so a
// test can look for a shape rather than index into argv.
func flatten(calls []execCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, strings.TrimSpace(c.Cmd+" "+strings.Join(c.Args, " ")))
	}
	return out
}

func ranAny(cmds []string, substr string) bool {
	for _, c := range cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// TestTunnelIsTornDownWhenIPv6IsOff is the regression test. Start and
// stop were decided at the call sites on Mode alone, so turning IPv6 off
// while the mode stayed at 6in4 skipped the teardown, because the mode
// had not changed, and still ran the bring-up. The sit interface and the
// default route survived, and every WAN reconnect rebuilt them, leaving
// the router persistently IPv6-active against the operator's setting.
func TestTunnelIsTornDownWhenIPv6IsOff(t *testing.T) {
	cmds := applyWithEnabled(t, "off", "6in4")

	if ranAny(cmds, "ip tunnel add") {
		t.Errorf("the tunnel was created with IPv6 switched off:\n%s", strings.Join(cmds, "\n"))
	}
	if !ranAny(cmds, "ip tunnel del") {
		t.Errorf("the tunnel was not torn down:\n%s", strings.Join(cmds, "\n"))
	}
	if !ranAny(cmds, "route del ::/0") {
		t.Errorf("the default route was left in place:\n%s", strings.Join(cmds, "\n"))
	}
}

// TestTunnelIsTornDownWhenTheModeIsNot6in4 keeps the mode crossover
// working through the same entry point.
func TestTunnelIsTornDownWhenTheModeIsNot6in4(t *testing.T) {
	cmds := applyWithEnabled(t, "auto", "dhcpv6-pd")

	if ranAny(cmds, "ip tunnel add") {
		t.Errorf("the tunnel was created in dhcpv6-pd mode:\n%s", strings.Join(cmds, "\n"))
	}
	if !ranAny(cmds, "ip tunnel del") {
		t.Error("the tunnel was not torn down on a mode swap")
	}
}

// TestTunnelComesUpWhenEnabledAndSelected keeps the guard from breaking
// the case it exists to permit.
func TestTunnelComesUpWhenEnabledAndSelected(t *testing.T) {
	for _, enabled := range []string{"auto", "on"} {
		cmds := applyWithEnabled(t, enabled, "6in4")
		if !ranAny(cmds, "ip tunnel add") {
			t.Errorf("enabled=%q did not bring the tunnel up:\n%s", enabled, strings.Join(cmds, "\n"))
		}
	}
}

// TestDisabledTunnelStaysDownAcrossReconnects is the operator-visible
// consequence: the PPPoE on-connect hook used to rebuild the tunnel on
// every reconnect regardless of the flag.
func TestDisabledTunnelStaysDownAcrossReconnects(t *testing.T) {
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := newSixInFourTestConfig(t)
	cfg.IPv6.Enabled = "off"
	cfg.IPv6.Mode = "6in4"

	svc := services.NewSixInFourService(cfg)
	svc.SetStatePathForTest(t.TempDir() + "/tunnel.json")
	svc.SetLocalIPv4ForTest("198.51.100.7")

	ctx := context.Background()
	for range 3 {
		if err := svc.ApplyConfig(ctx); err != nil {
			t.Fatalf("ApplyConfig: %v", err)
		}
	}

	if ranAny(flatten(agent.execCallsCopy()), "ip tunnel add") {
		t.Error("a reconnect rebuilt a tunnel the operator had switched off")
	}
	if svc.IsRunning() {
		t.Error("the service reports the tunnel running while IPv6 is off")
	}
}

// TestApplyConfigLeavesNoTunnelRunningFlag guards the status card,
// which reads IsRunning.
func TestApplyConfigLeavesNoTunnelRunningFlag(t *testing.T) {
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := newSixInFourTestConfig(t)
	cfg.IPv6.Enabled = "auto"
	cfg.IPv6.Mode = "6in4"

	svc := services.NewSixInFourService(cfg)
	svc.SetStatePathForTest(t.TempDir() + "/tunnel.json")
	svc.SetLocalIPv4ForTest("198.51.100.7")

	ctx := context.Background()
	if err := svc.ApplyConfig(ctx); err != nil {
		t.Fatalf("apply while enabled: %v", err)
	}
	if !svc.IsRunning() {
		t.Fatal("the tunnel did not come up while enabled")
	}

	cfg.IPv6.Enabled = "off"
	if err := svc.ApplyConfig(ctx); err != nil {
		t.Fatalf("apply after disabling: %v", err)
	}
	if svc.IsRunning() {
		t.Error("the tunnel still reports running after being disabled")
	}
}
