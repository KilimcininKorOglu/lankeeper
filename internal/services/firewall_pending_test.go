package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// pendingStatePath points the service at a temp file and restores the
// environment afterwards.
func pendingStatePath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "firewall-pending.json")
	t.Setenv("LANKEEPER_FIREWALL_STATE", p)
	return p
}

func writePendingState(t *testing.T, path, snapshot string, appliedAt time.Time) {
	t.Helper()
	data, err := json.Marshal(firewallPendingState{Snapshot: snapshot, AppliedAt: appliedAt})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func newTestFirewallService(t *testing.T) *FirewallService {
	t.Helper()
	svc, err := NewFirewallServiceFromFS(&config.Config{}, "flush ruleset\n")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	// A restored change arms a timer for the rest of the confirmation
	// window, and it fires from its own goroutine into netutil.Run.
	// Left running it outlives the test and reads the process-global
	// agent client while a later test's cleanup writes it.
	t.Cleanup(svc.stopWatchdog)
	return svc
}

// TestPersistPendingStateWritesSnapshot covers the record the watchdog
// needs to survive a restart. The web unit runs Restart=always with
// RestartSec=3, so a restart inside the 30 s window is realistic, and
// without this record the snapshot and timer die with the process.
func TestPersistPendingStateWritesSnapshot(t *testing.T) {
	path := pendingStatePath(t)
	svc := newTestFirewallService(t)

	svc.persistPendingState("table inet filter {}")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	var state firewallPendingState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if state.Snapshot != "table inet filter {}" {
		t.Errorf("snapshot = %q, want the ruleset text", state.Snapshot)
	}
	if state.AppliedAt.IsZero() {
		t.Error("appliedAt not recorded, remaining window cannot be computed")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("state file mode = %o, want 600", got)
	}
}

// TestPersistPendingStateSkipsEmptySnapshot avoids leaving a record that
// restore would only have to discard.
func TestPersistPendingStateSkipsEmptySnapshot(t *testing.T) {
	path := pendingStatePath(t)
	svc := newTestFirewallService(t)

	svc.persistPendingState("")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("empty snapshot produced a state file")
	}
}

// TestClearPendingStateRemovesFile backs Confirm and Rollback.
func TestClearPendingStateRemovesFile(t *testing.T) {
	path := pendingStatePath(t)
	svc := newTestFirewallService(t)

	svc.persistPendingState("snapshot")
	svc.clearPendingState()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("state file survived clearPendingState")
	}
}

// TestConfirmClearsPendingState exercises the operator-confirmed path
// end to end.
func TestConfirmClearsPendingState(t *testing.T) {
	path := pendingStatePath(t)
	svc := newTestFirewallService(t)

	svc.persistPendingState("snapshot")
	svc.Confirm()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Confirm left a pending record behind")
	}
}

// TestRestorePendingChangeRearmsWatchdog is the regression test: a
// process that did not apply the change must still pick up the pending
// one and report it.
func TestRestorePendingChangeRearmsWatchdog(t *testing.T) {
	path := pendingStatePath(t)
	// Applied 5 seconds ago, so ~25 seconds remain and nothing fires
	// during the test.
	writePendingState(t, path, "table inet filter {}", time.Now().Add(-5*time.Second))

	svc := newTestFirewallService(t)

	if !svc.HasPendingChange() {
		t.Fatal("restored service does not report a pending change")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file removed during restore: %v", err)
	}
}

// TestRestoreIgnoresMissingState is the ordinary boot path.
func TestRestoreIgnoresMissingState(t *testing.T) {
	pendingStatePath(t)
	svc := newTestFirewallService(t)

	if svc.HasPendingChange() {
		t.Error("service reports a pending change with no state file")
	}
}

// TestRestoreIgnoresCorruptState keeps a damaged file from breaking
// startup, since the constructor runs on every boot.
func TestRestoreIgnoresCorruptState(t *testing.T) {
	path := pendingStatePath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc := newTestFirewallService(t)

	if svc.HasPendingChange() {
		t.Error("corrupt state produced a pending change")
	}
}

// TestRestoreDropsSnapshotlessState avoids re-reading a record that can
// never be rolled back to.
func TestRestoreDropsSnapshotlessState(t *testing.T) {
	path := pendingStatePath(t)
	writePendingState(t, path, "", time.Now())

	svc := newTestFirewallService(t)

	if svc.HasPendingChange() {
		t.Error("state without a snapshot produced a pending change")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("useless state file was not discarded")
	}
}

// execRecordingAgent captures the commands the rollback issues so the
// expired-window path can be asserted without running nft for real.
type execRecordingAgent struct {
	mu    sync.Mutex
	calls []string
}

func (a *execRecordingAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method == "exec.run" {
		raw, _ := json.Marshal(params)
		var p struct {
			Cmd  string   `json:"cmd"`
			Args []string `json:"args"`
		}
		_ = json.Unmarshal(raw, &p)
		a.mu.Lock()
		a.calls = append(a.calls, p.Cmd+" "+strings.Join(p.Args, " "))
		a.mu.Unlock()
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func (a *execRecordingAgent) snapshotCalls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// TestRestoreRollsBackExpiredWindow is the core of the fix: a record
// older than the confirmation window must revert immediately on the next
// start, not grant a fresh 30 seconds.
//
// Mutates the process-global agent client, so no t.Parallel here.
func TestRestoreRollsBackExpiredWindow(t *testing.T) {
	path := pendingStatePath(t)
	writePendingState(t, path, "table inet filter {}", time.Now().Add(-firewallConfirmWindow-time.Minute))

	agent := &execRecordingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	newTestFirewallService(t)

	// The rollback runs from the timer goroutine with a zero delay.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls := agent.snapshotCalls()
	var flushed, reapplied bool
	for _, c := range calls {
		if strings.HasPrefix(c, "nft flush ruleset") {
			flushed = true
		}
		if strings.HasPrefix(c, "nft -f ") {
			reapplied = true
		}
	}
	if !flushed || !reapplied {
		t.Errorf("expired window did not roll back (flush=%v reapply=%v); calls: %v",
			flushed, reapplied, calls)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("state file survived the watchdog rollback; the next start would replay it")
	}
}

// TestAStoppedWatchdogIssuesNoCommands is the regression test. The
// timer restorePendingChange arms ran for the rest of the confirmation
// window with nothing able to stop it, so it outlived whoever built the
// service and fired into netutil.Run from its own goroutine. In the
// suite that surfaced as an intermittent data race: the rollback read
// the process-global agent client while an unrelated test's cleanup
// wrote it.
//
// Mutates the process-global agent client, so no t.Parallel here.
func TestAStoppedWatchdogIssuesNoCommands(t *testing.T) {
	path := pendingStatePath(t)
	// Barely inside the window, so an un-stopped timer fires well
	// within the wait below.
	writePendingState(t, path, "table inet filter {}",
		time.Now().Add(-firewallConfirmWindow+150*time.Millisecond))

	agent := &execRecordingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := newTestFirewallService(t)
	if !svc.HasPendingChange() {
		t.Fatal("the restored service reports no pending change, so nothing was armed")
	}

	svc.stopWatchdog()

	time.Sleep(time.Second)

	if calls := agent.snapshotCalls(); len(calls) != 0 {
		t.Errorf("a stopped watchdog still ran commands: %v", calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("stopping the watchdog dropped the record that re-arms it: %v", err)
	}
}
