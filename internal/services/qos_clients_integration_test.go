package services_test

import (
	"context"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestRebuildClientCountersEmitsExpectedNftCalls drives
// RebuildClientCounters through the production agent path and
// asserts on the recorded exec.run + file.write calls. Verifies:
//
//   - the lankeeper_qos table script reaches `nft -f -` on stdin, so no
//     scratch file sits at a name another local account could claim.
//   - nft is invoked exactly once.
//   - the rendered script flushes the table before declaring it,
//     so consecutive rebuilds remain idempotent.
//   - duplicate MACs in the lease list collapse into a single
//     counter pair.
func TestRebuildClientCountersEmitsExpectedNftCalls(t *testing.T) {
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	cfg.QoS.Enabled = true
	cfg.QoS.Profile = "cake"
	svc := services.NewQoSService(cfg)

	leases := []services.Lease{
		{MAC: "AA:BB:CC:DD:EE:01", IP: "10.10.10.5", Hostname: "alice"},
		{MAC: "aa:bb:cc:dd:ee:01", IP: "10.10.10.5", Hostname: "alice"}, // dup, different case
		{MAC: "AA:BB:CC:DD:EE:02", IP: "10.10.10.6", Hostname: "bob"},
	}

	if err := svc.RebuildClientCounters(context.Background(), leases); err != nil {
		t.Fatalf("RebuildClientCounters: %v", err)
	}

	script := assertSingleNftLoad(t, agent)
	for _, want := range []string{"delete table inet lankeeper_qos", "table inet lankeeper_qos"} {
		if !strings.Contains(script, want) {
			t.Errorf("script must carry %q so a reapply flushes and redeclares the table, got:\n%s", want, script)
		}
	}
	// Two unique MACs → two upload rules on the MAC, two download rules
	// on the leased address, and four counters.
	for want, n := range map[string]int{"ether saddr ": 2, "ip daddr 10.10.10.": 2, "counter cli_": 4} {
		if got := strings.Count(script, want); got != n {
			t.Errorf("expected %d %q entries (duplicate MACs collapse), got %d in:\n%s", n, want, got, script)
		}
	}
}

// assertSingleNftLoad checks that the only exec.run was one `nft -f -`
// and returns the script it read from stdin.
func assertSingleNftLoad(t *testing.T, agent *fakeAgent) string {
	t.Helper()
	calls := agent.execCallsCopy()
	if len(calls) != 1 || calls[0].Cmd != "nft" {
		t.Fatalf("expected exactly one nft invocation, got %+v", calls)
	}
	if args := calls[0].Args; len(args) != 2 || args[0] != "-f" || args[1] != "-" {
		t.Errorf("expected `nft -f -`, got: %v", args)
	}
	if len(agent.writeLog) != 0 {
		t.Errorf("the script was also written to a file: %+v", agent.writeLog)
	}
	return calls[0].Stdin
}

// TestRebuildClientCountersEmptyLeasesFlushes asserts that an empty
// lease list still produces a "delete table" line so a previous
// snapshot is fully torn down — but no per-MAC rules are emitted.
func TestRebuildClientCountersEmptyLeasesFlushes(t *testing.T) {
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	svc := services.NewQoSService(cfg)

	if err := svc.RebuildClientCounters(context.Background(), nil); err != nil {
		t.Fatalf("RebuildClientCounters(nil): %v", err)
	}

	script := assertSingleNftLoad(t, agent)
	if !strings.Contains(script, "delete table inet lankeeper_qos") {
		t.Errorf("empty rebuild must still flush, got:\n%s", script)
	}
	if strings.Contains(script, "ether saddr ") || strings.Contains(script, "ether daddr ") {
		t.Errorf("empty rebuild must not emit per-MAC rules, got:\n%s", script)
	}
}
