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
//   - the lankeeper_qos table script is written to a /tmp/ path
//     covered by the agent's file-write whitelist.
//   - `nft -f <script>` is invoked exactly once.
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

	// Inspect the written script.
	if !agent.wroteFile("lankeeper-qos.nft") {
		t.Fatalf("expected qos nft script under /tmp/, writes: %+v", agent.writeLog)
	}

	script := agent.lastWrite("lankeeper-qos.nft")
	for _, want := range []string{"delete table inet lankeeper_qos", "table inet lankeeper_qos"} {
		if !strings.Contains(script, want) {
			t.Errorf("script must carry %q so a reapply flushes and redeclares the table, got:\n%s", want, script)
		}
	}
	// Two unique MACs → two saddr + two daddr rules + four counters.
	for want, n := range map[string]int{"ether saddr ": 2, "ether daddr ": 2, "counter cli_": 4} {
		if got := strings.Count(script, want); got != n {
			t.Errorf("expected %d %q entries (duplicate MACs collapse), got %d in:\n%s", n, want, got, script)
		}
	}

	assertSingleNftLoad(t, agent, "lankeeper-qos.nft")
}

// assertSingleNftLoad checks that the only exec.run was one `nft -f` of
// the script whose path ends in suffix.
func assertSingleNftLoad(t *testing.T, agent *fakeAgent, suffix string) {
	t.Helper()
	calls := agent.execCallsCopy()
	if len(calls) != 1 || calls[0].Cmd != "nft" {
		t.Fatalf("expected exactly one nft invocation, got %+v", calls)
	}
	args := calls[0].Args
	if len(args) != 2 || args[0] != "-f" || !strings.HasSuffix(args[1], suffix) {
		t.Errorf("expected `nft -f .../%s`, got: %v", suffix, args)
	}
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

	script := agent.lastWrite("lankeeper-qos.nft")
	if !strings.Contains(script, "delete table inet lankeeper_qos") {
		t.Errorf("empty rebuild must still flush, got:\n%s", script)
	}
	if strings.Contains(script, "ether saddr ") || strings.Contains(script, "ether daddr ") {
		t.Errorf("empty rebuild must not emit per-MAC rules, got:\n%s", script)
	}
}
