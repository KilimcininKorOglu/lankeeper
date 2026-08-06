package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// countCalls returns how many recorded commands start with prefix.
func countCalls(calls []string, prefix string) int {
	var n int
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// TestApplyRefusesWhileAChangeIsPending is the regression test. Apply
// overwrote s.change without stopping the previous watchdog, so the
// orphaned timer kept running against the older snapshot. The IPv6
// lease hook applies and confirms on every lease event, so a routine
// renewal during an operator's confirmation window would drop that
// operator's change from the service while leaving its watchdog armed,
// and the orphan then reverted both changes 30 seconds later.
//
// Mutates the process-global agent client, so no t.Parallel here.
func TestApplyRefusesWhileAChangeIsPending(t *testing.T) {
	pendingStatePath(t)
	agent := &execRecordingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := newTestFirewallService(t)
	ctx := context.Background()

	if err := svc.Apply(ctx); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	applied := countCalls(agent.snapshotCalls(), "nft -f ")
	if applied != 1 {
		t.Fatalf("first apply issued %d nft -f calls, want 1", applied)
	}

	err := svc.Apply(ctx)
	if !errors.Is(err, ErrChangePending) {
		t.Fatalf("second apply returned %v, want ErrChangePending", err)
	}
	if got := countCalls(agent.snapshotCalls(), "nft -f "); got != applied {
		t.Errorf("the refused apply still touched the ruleset (%d nft -f calls, want %d)", got, applied)
	}

	// The operator's change must still be the one the service holds.
	svc.mu.RLock()
	pending := svc.change
	svc.mu.RUnlock()
	if pending == nil {
		t.Error("the pending change was dropped by the refused apply")
	}

	// Confirming clears the guard so the next apply goes through.
	svc.Confirm()
	if err := svc.Apply(ctx); err != nil {
		t.Errorf("apply after confirm: %v", err)
	}
	svc.Confirm()
}

// TestWatchdogRollbackClearsThePendingChange keeps the new guard from
// becoming permanent. The watchdog callback did not reset s.change, so
// once a change rolled back on its own every later apply would be
// refused for the lifetime of the process.
func TestWatchdogRollbackClearsThePendingChange(t *testing.T) {
	pendingStatePath(t)
	agent := &execRecordingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := newTestFirewallService(t)
	ac := netutil.NewAtomicChangeWithSnapshot("firewall", "table inet filter {}")

	svc.mu.Lock()
	svc.change = ac
	svc.mu.Unlock()
	svc.armWatchdog(ac, time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.RLock()
		cleared := svc.change == nil
		svc.mu.RUnlock()
		if cleared {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	svc.mu.RLock()
	pending := svc.change
	svc.mu.RUnlock()
	if pending != nil {
		t.Fatal("the watchdog rolled back but left the change in place; every later apply would be refused")
	}
	if countCalls(agent.snapshotCalls(), "nft flush ruleset") == 0 {
		t.Fatal("the watchdog did not roll back at all")
	}

	if err := svc.Apply(context.Background()); err != nil {
		t.Errorf("apply after a watchdog rollback: %v", err)
	}
	svc.Confirm()
}

// TestSettledChangeSurvivesALateWatchdog covers the race the guard
// closes. Stopping a timer that has already fired does not unschedule
// its callback, so a Confirm landing on the deadline could previously
// be followed by a rollback of the ruleset it just confirmed.
//
// The service lock is held while the callback is in flight, which is
// exactly the window the real race opens.
func TestSettledChangeSurvivesALateWatchdog(t *testing.T) {
	pendingStatePath(t)
	agent := &execRecordingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := newTestFirewallService(t)
	ac := netutil.NewAtomicChangeWithSnapshot("firewall", "table inet filter {}")

	svc.mu.Lock()
	svc.change = ac
	// Fires at once; the callback then blocks on the lock this test
	// holds, which puts it in the same position as a watchdog that
	// fired a moment before Confirm ran.
	svc.armWatchdog(ac, 0)
	time.Sleep(50 * time.Millisecond)
	// Settle the change as Confirm would.
	svc.change = nil
	svc.mu.Unlock()

	// Give the released callback time to do the wrong thing.
	time.Sleep(200 * time.Millisecond)

	if n := countCalls(agent.snapshotCalls(), "nft flush ruleset"); n != 0 {
		t.Errorf("a settled change was rolled back by its late watchdog (%d flush calls)", n)
	}
}
