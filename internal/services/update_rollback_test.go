package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// rollbackAgent records every privileged command so a test can count
// binary swaps and service restarts.
type rollbackAgent struct {
	mu    sync.Mutex
	calls []string
}

func (a *rollbackAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return []byte(`{}`), nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var req struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.calls = append(a.calls, strings.TrimSpace(req.Cmd+" "+strings.Join(req.Args, " ")))
	a.mu.Unlock()
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func (a *rollbackAgent) count(substr string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, c := range a.calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

// newPendingUpdateService builds a service that believes an update is
// waiting for confirmation, which is the only state a rollback is
// meaningful in.
func newPendingUpdateService(t *testing.T) (*UpdateService, *rollbackAgent) {
	t.Helper()
	agent := &rollbackAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := &UpdateService{
		binaryPath:      "/usr/local/bin/lankeeper",
		statePath:       t.TempDir() + "/update-state.json",
		pendingVersion:  "v9.9.9",
		previousVersion: "v9.9.8",
		backupBinary:    "/usr/local/bin/lankeeper.bak",
	}
	return svc, agent
}

// TestRollbackRefusesWithNoPendingUpdate is the regression test.
// Rollback never consulted the pending state and substituted a fixed
// backup path that every apply writes and no rollback removed, so a
// replayed request after a completed rollback found a usable backup and
// repeated the binary swap plus a service restart.
func TestRollbackRefusesWithNoPendingUpdate(t *testing.T) {
	agent := &rollbackAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := &UpdateService{
		binaryPath: "/usr/local/bin/lankeeper",
		statePath:  t.TempDir() + "/update-state.json",
	}

	err := svc.Rollback(context.Background())
	if err == nil {
		t.Fatal("a rollback with nothing pending was accepted")
	}
	if !errors.Is(err, ErrNoPendingUpdate) {
		t.Errorf("got %v, want ErrNoPendingUpdate", err)
	}
	if n := agent.count("cp -f"); n != 0 {
		t.Errorf("the binary was swapped %d times with nothing pending", n)
	}
	if n := agent.count("systemctl restart"); n != 0 {
		t.Errorf("the service was restarted %d times with nothing pending", n)
	}
}

// TestReplayedRollbackIsRefused is the concrete scenario: the second
// submission must not repeat the swap.
func TestReplayedRollbackIsRefused(t *testing.T) {
	svc, agent := newPendingUpdateService(t)
	ctx := context.Background()

	if err := svc.Rollback(ctx); err != nil {
		t.Fatalf("first rollback: %v", err)
	}
	swapsAfterFirst := agent.count("cp -f")
	if swapsAfterFirst == 0 {
		t.Fatal("the first rollback did nothing; the test proves nothing")
	}

	if err := svc.Rollback(ctx); !errors.Is(err, ErrNoPendingUpdate) {
		t.Fatalf("the replayed rollback returned %v, want ErrNoPendingUpdate", err)
	}
	if got := agent.count("cp -f"); got != swapsAfterFirst {
		t.Errorf("the replay performed %d extra binary swaps", got-swapsAfterFirst)
	}
	if got := agent.count("systemctl restart"); got != 1 {
		t.Errorf("the service was restarted %d times, want 1", got)
	}
}

// TestRollbackRemovesTheBackup covers the second half of the root
// cause. Only ConfirmUpdate deleted the backup, so after a rollback the
// file survived at a predictable path and a later apply refreshed it,
// which is what turned a replay into an unannounced downgrade.
func TestRollbackRemovesTheBackup(t *testing.T) {
	svc, agent := newPendingUpdateService(t)

	if err := svc.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := agent.count("rm -f /usr/local/bin/lankeeper.bak"); n != 1 {
		t.Errorf("the backup binary was removed %d times, want 1", n)
	}
}

// TestRollbackWithoutARecordedBackupIsRefused removes the fallback that
// made the fixed path reachable at all. A pending version whose backup
// is unknown has nothing it can be sure it is restoring.
func TestRollbackWithoutARecordedBackupIsRefused(t *testing.T) {
	agent := &rollbackAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := &UpdateService{
		binaryPath:     "/usr/local/bin/lankeeper",
		statePath:      t.TempDir() + "/update-state.json",
		pendingVersion: "v9.9.9",
	}

	if err := svc.Rollback(context.Background()); !errors.Is(err, ErrNoPendingUpdate) {
		t.Fatalf("got %v, want ErrNoPendingUpdate", err)
	}
	if n := agent.count("cp -f"); n != 0 {
		t.Errorf("a guessed backup path was used %d times", n)
	}
}

// TestRollbackClearsThePendingState keeps the state machine consistent,
// which is what the refusal above relies on.
func TestRollbackClearsThePendingState(t *testing.T) {
	svc, _ := newPendingUpdateService(t)

	if err := svc.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if svc.HasPendingUpdate() {
		t.Error("the service still reports a pending update after rolling back")
	}
	if svc.PendingVersion() != "" {
		t.Errorf("pending version = %q, want empty", svc.PendingVersion())
	}
}
