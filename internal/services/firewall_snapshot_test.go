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

// snapshotAgent fails the snapshot command and records everything else,
// so a test can see whether the apply continued past the failure.
type snapshotAgent struct {
	mu           sync.Mutex
	calls        []string
	failSnapshot bool
	emptyRuleset bool
}

func (a *snapshotAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
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
	line := strings.TrimSpace(req.Cmd + " " + strings.Join(req.Args, " "))

	a.mu.Lock()
	a.calls = append(a.calls, line)
	failSnap := a.failSnapshot
	empty := a.emptyRuleset
	a.mu.Unlock()

	if line == "nft list ruleset" {
		if failSnap {
			return nil, errors.New("nft: command not found")
		}
		if empty {
			return json.Marshal(struct {
				Stdout   string `json:"stdout"`
				Stderr   string `json:"stderr"`
				ExitCode int    `json:"exitCode"`
			}{Stdout: ""})
		}
		return json.Marshal(struct {
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
			ExitCode int    `json:"exitCode"`
		}{Stdout: "table inet filter {\n}\n"})
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func (a *snapshotAgent) ran(substr string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func newSnapshotTest(t *testing.T, agent *snapshotAgent) *FirewallService {
	t.Helper()
	pendingStatePath(t)
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
	return newTestFirewallService(t)
}

// TestApplyAbortsWhenTheSnapshotFails is the regression test. Apply
// logged the snapshot error and carried on, so the rules were validated,
// applied and a watchdog armed against an empty snapshot. Rollback
// refuses to act without one, so on exactly the apply where the
// anti-lockout watchdog matters most, a ruleset that locks the operator
// out would never be reverted.
func TestApplyAbortsWhenTheSnapshotFails(t *testing.T) {
	agent := &snapshotAgent{failSnapshot: true}
	svc := newSnapshotTest(t, agent)

	err := svc.Apply(context.Background())
	if err == nil {
		t.Fatal("the apply succeeded with no rollback snapshot")
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("got %v, want an error naming the snapshot", err)
	}

	// The safe outcome is that the previous, working ruleset is left
	// alone rather than replaced with one nothing can revert.
	if agent.ran("nft -f") {
		t.Error("the ruleset was applied despite having no rollback snapshot")
	}
	if svc.HasPendingChange() {
		t.Error("a watchdog was armed for an apply that never happened")
	}
}

// TestApplySucceedsOnAFreshSystem is the other half of the same root
// cause. On a system with no rules `nft list ruleset` succeeds and
// prints nothing, so the snapshot was stored as the empty string, which
// Rollback treats as "nothing was ever captured" and refuses to act on.
// The first apply on a fresh install is precisely the one an operator
// most needs to be able to undo.
func TestApplySucceedsOnAFreshSystem(t *testing.T) {
	agent := &snapshotAgent{emptyRuleset: true}
	svc := newSnapshotTest(t, agent)

	if err := svc.Apply(context.Background()); err != nil {
		t.Fatalf("apply on an empty ruleset: %v", err)
	}
	t.Cleanup(svc.Confirm)

	if !svc.HasPendingChange() {
		t.Fatal("no watchdog was armed")
	}

	// The recorded snapshot must be something Rollback will act on.
	if err := svc.Rollback(context.Background()); err != nil {
		t.Errorf("rollback after a fresh-system apply: %v", err)
	}
	if !agent.ran("nft flush ruleset") {
		t.Error("the rollback did no work, so the safety net was absent")
	}
}

// TestSnapshotOfAnEmptyRulesetIsApplicable pins the representation
// itself: it has to be non-empty so it survives the "nothing captured"
// check, and valid nft input so applying it after the flush is a no-op
// rather than a parse error.
func TestSnapshotOfAnEmptyRulesetIsApplicable(t *testing.T) {
	agent := &snapshotAgent{emptyRuleset: true}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	snap := ac.GetSnapshot()
	if strings.TrimSpace(snap) == "" {
		t.Fatal("an empty ruleset produced an empty snapshot, which Rollback refuses to use")
	}
	for _, line := range strings.Split(strings.TrimSpace(snap), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			t.Errorf("the placeholder carries a live directive: %q", line)
		}
	}
}

// TestSnapshotKeepsARealRulesetVerbatim guards against the placeholder
// leaking into the normal case.
func TestSnapshotKeepsARealRulesetVerbatim(t *testing.T) {
	agent := &snapshotAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	ac := netutil.NewAtomicChange("firewall")
	if err := ac.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if got := ac.GetSnapshot(); got != "table inet filter {\n}\n" {
		t.Errorf("snapshot = %q, want the ruleset verbatim", got)
	}
}
