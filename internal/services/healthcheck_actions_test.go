package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// cmdLogAgent records the argv of every exec.run and answers success.
type cmdLogAgent struct {
	mu   sync.Mutex
	argv []string
}

func (a *cmdLogAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return nil, fmt.Errorf("unhandled %s", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.argv = append(a.argv, strings.Join(append([]string{p.Cmd}, p.Args...), " "))
	a.mu.Unlock()
	return json.Marshal(map[string]any{"stdout": "", "stderr": "", "exitCode": 0})
}

// TestExecuteActionsStopsAtTheFirstActionThatSucceeds pins the recovery
// sequence: an unknown action and a failing action are skipped, the
// first action that succeeds is recorded, starts the cooldown and ends
// the sequence.
func TestExecuteActionsStopsAtTheFirstActionThatSucceeds(t *testing.T) {
	agent := &cmdLogAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewHealthCheckService(&config.Config{})
	svc.results["wan"] = &CheckResult{Name: "wan", FailureCount: 3}

	check := config.HealthCheckEntry{
		Name:      "wan",
		Interface: "missing",
		Actions: []config.HealthCheckAction{
			{Type: "bogus"},
			{Type: "restartInterface"},
			{Type: "rebootSystem"},
			{Type: "restartPppoe"},
		},
	}
	svc.executeActions(context.Background(), check, time.Hour)

	agent.mu.Lock()
	argv := append([]string(nil), agent.argv...)
	agent.mu.Unlock()
	if len(argv) != 1 || argv[0] != "systemctl reboot" {
		t.Errorf("commands = %q, want only the reboot", argv)
	}

	svc.mu.RLock()
	r := *svc.results["wan"]
	svc.mu.RUnlock()
	if r.LastAction != "rebootSystem" || !r.InCooldown || r.FailureCount != 0 {
		t.Errorf("result = %+v", r)
	}
}

// TestExecuteActionsReturnsWhenCancelledDuringADelay keeps a stopped
// service from running a remediation action after its delay.
func TestExecuteActionsReturnsWhenCancelledDuringADelay(t *testing.T) {
	agent := &cmdLogAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewHealthCheckService(&config.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	check := config.HealthCheckEntry{
		Name:    "wan",
		Actions: []config.HealthCheckAction{{Type: "rebootSystem", Delay: "1h"}},
	}
	svc.executeActions(ctx, check, time.Hour)

	if len(agent.argv) != 0 {
		t.Errorf("commands ran after cancellation: %q", agent.argv)
	}
}
