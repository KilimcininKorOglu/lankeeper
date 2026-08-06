package netutil_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// scriptedAgent records every exec.run it is handed and answers with a
// canned stdout, so the whole lifecycle can be driven without an nft
// binary. Every command in this package routes through netutil.Run,
// which is what makes that possible.
type scriptedAgent struct {
	mu     sync.Mutex
	calls  []string
	stdout map[string]string
	fail   map[string]bool
}

func newScriptedAgent() *scriptedAgent {
	return &scriptedAgent{
		stdout: make(map[string]string),
		fail:   make(map[string]bool),
	}
}

func (a *scriptedAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return []byte(`{}`), nil
	}

	raw, _ := json.Marshal(params)
	var p struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	_ = json.Unmarshal(raw, &p)
	line := strings.TrimSpace(p.Cmd + " " + strings.Join(p.Args, " "))

	a.mu.Lock()
	a.calls = append(a.calls, line)
	out := a.stdout[line]
	shouldFail := a.fail[line]
	a.mu.Unlock()

	if shouldFail {
		// The real agent reports a failed command as an RPC error, not
		// as a result carrying a non-zero exit code, and Client.Call
		// turns that into a Go error. Returning a result here instead
		// would make the fake disagree with production and the test
		// would assert nothing.
		return nil, errors.New("exec nft: exit status 1")
	}
	resp := map[string]any{"stdout": out, "stderr": "", "exitCode": 0}
	b, _ := json.Marshal(resp)
	return b, nil
}

func (a *scriptedAgent) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *scriptedAgent) sawPrefix(prefix string) bool {
	for _, c := range a.recorded() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// useScriptedAgent installs the fake for one test. agentClient is
// process-global, so no t.Parallel in this file.
func useScriptedAgent(t *testing.T) *scriptedAgent {
	t.Helper()
	a := newScriptedAgent()
	netutil.SetAgentClient(a)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
	return a
}

// TestSnapshotCapturesTheLiveRuleset covers the first step. Everything
// downstream depends on it: a snapshot that is not captured is a
// rollback that cannot happen.
func TestSnapshotCapturesTheLiveRuleset(t *testing.T) {
	agent := useScriptedAgent(t)
	agent.stdout["nft list ruleset"] = "table inet filter { chain input {} }"

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if got := ac.GetSnapshot(); got != "table inet filter { chain input {} }" {
		t.Errorf("snapshot = %q, want the live ruleset text", got)
	}
}

// TestSnapshotRejectsUnknownService pins the guard on the service
// dispatch. The field is generalised but has exactly one real value.
func TestSnapshotRejectsUnknownService(t *testing.T) {
	useScriptedAgent(t)

	ac := netutil.NewAtomicChange("nftables-typo")
	err := ac.Snapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Errorf("expected an unknown-service error, got %v", err)
	}
}

// TestValidateRunsCheckModeBeforeApply is the difference between a
// rejected ruleset and a locked-out operator: nft -c parses without
// committing.
func TestValidateRunsCheckModeBeforeApply(t *testing.T) {
	agent := useScriptedAgent(t)

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Validate(context.Background(), "/tmp/candidate.conf"); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if !agent.sawPrefix("nft -c -f /tmp/candidate.conf") {
		t.Errorf("validate did not run nft in check mode; calls: %v", agent.recorded())
	}
	if agent.sawPrefix("nft -f /tmp/candidate.conf") {
		t.Error("validate committed the ruleset instead of only checking it")
	}
}

// TestValidateSurfacesARejectedRuleset keeps a bad candidate from
// reaching Apply.
func TestValidateSurfacesARejectedRuleset(t *testing.T) {
	agent := useScriptedAgent(t)
	agent.fail["nft -c -f /tmp/bad.conf"] = true

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Validate(context.Background(), "/tmp/bad.conf"); err == nil {
		t.Error("a ruleset nft rejected was reported as valid")
	}
}

// TestApplyCommitsTheCandidate covers the step that can lock the
// operator out.
func TestApplyCommitsTheCandidate(t *testing.T) {
	agent := useScriptedAgent(t)

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Apply(context.Background(), "/tmp/candidate.conf"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if !agent.sawPrefix("nft -f /tmp/candidate.conf") {
		t.Errorf("apply did not commit the ruleset; calls: %v", agent.recorded())
	}
}

// TestRollbackFlushesThenReappliesTheSnapshot is the core of the safety
// net. A partial rollback, flush without re-apply, would leave the
// router with no ruleset at all, which is worse than the change being
// rolled back.
func TestRollbackFlushesThenReappliesTheSnapshot(t *testing.T) {
	agent := useScriptedAgent(t)
	agent.stdout["nft list ruleset"] = "table inet filter { chain input { policy accept } }"

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := ac.Apply(context.Background(), "/tmp/candidate.conf"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := ac.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	calls := agent.recorded()
	flushAt, reapplyAt := -1, -1
	for i, c := range calls {
		if strings.HasPrefix(c, "nft flush ruleset") {
			flushAt = i
		}
		// The rollback writes the snapshot to its own temp file, so
		// match the command rather than the candidate path.
		if strings.HasPrefix(c, "nft -f /tmp/nft-rollback-") {
			reapplyAt = i
		}
	}

	if flushAt < 0 {
		t.Fatalf("rollback never flushed; calls: %v", calls)
	}
	if reapplyAt < 0 {
		t.Fatalf("rollback flushed but never re-applied the snapshot, leaving no ruleset; calls: %v", calls)
	}
	if reapplyAt < flushAt {
		t.Errorf("rollback re-applied before flushing; calls: %v", calls)
	}
}

// TestRollbackWithoutASnapshotIsRefused matters because a failed
// snapshot must not present itself as a working safety net.
func TestRollbackWithoutASnapshotIsRefused(t *testing.T) {
	agent := useScriptedAgent(t)

	ac := netutil.NewAtomicChange("firewall")
	err := ac.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no snapshot") {
		t.Errorf("expected a no-snapshot error, got %v", err)
	}
	if agent.sawPrefix("nft flush ruleset") {
		t.Error("rollback flushed the ruleset with nothing to restore")
	}
}

// TestRollbackAfterAFailedApply covers the ordering the firewall
// service relies on: a snapshot taken before a failed apply is still
// usable to restore.
func TestRollbackAfterAFailedApply(t *testing.T) {
	agent := useScriptedAgent(t)
	agent.stdout["nft list ruleset"] = "table inet filter {}"
	agent.fail["nft -f /tmp/candidate.conf"] = true

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := ac.Apply(context.Background(), "/tmp/candidate.conf"); err == nil {
		t.Fatal("a failing nft apply was reported as success")
	}
	if err := ac.Rollback(context.Background()); err != nil {
		t.Errorf("rollback after a failed apply: %v", err)
	}
}

// TestWatchdogFiresWhenConfirmIsMissed is the whole point of the
// mechanism: an operator who cannot reach the UI after a bad ruleset
// gets the previous one back without touching the box.
func TestWatchdogFiresWhenConfirmIsMissed(t *testing.T) {
	useScriptedAgent(t)

	ac := netutil.NewAtomicChange("firewall")

	fired := make(chan struct{})
	ac.StartWatchdog(20*time.Millisecond, func() error {
		close(fired)
		return nil
	})

	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog never fired, so an unconfirmed change would stand")
	}
}

// TestConfirmStopsTheWatchdog is the other half. Without it an
// operator who did confirm would still have the change reverted under
// them.
func TestConfirmStopsTheWatchdog(t *testing.T) {
	useScriptedAgent(t)

	ac := netutil.NewAtomicChange("firewall")

	var fired atomicBool
	ac.StartWatchdog(100*time.Millisecond, func() error {
		fired.set()
		return nil
	})
	ac.Confirm()

	time.Sleep(400 * time.Millisecond)
	if fired.get() {
		t.Error("watchdog rolled back a confirmed change")
	}
}

// TestRollbackStopsTheWatchdog keeps a manual rollback from being
// followed by a second, automatic one.
func TestRollbackStopsTheWatchdog(t *testing.T) {
	agent := useScriptedAgent(t)
	agent.stdout["nft list ruleset"] = "table inet filter {}"

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	var fired atomicBool
	ac.StartWatchdog(100*time.Millisecond, func() error {
		fired.set()
		return nil
	})
	if err := ac.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	time.Sleep(400 * time.Millisecond)
	if fired.get() {
		t.Error("watchdog fired after an explicit rollback")
	}
}

// atomicBool avoids a race between the watchdog goroutine and the test.
type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) set() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.v = true
}

func (b *atomicBool) get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}
