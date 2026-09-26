package services

import (
	"context"
	"errors"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// Each update buffers every list and reloads Unbound, so a second
// request while one runs is refused rather than run in parallel, and the
// guard is released once the run ends.
func TestUpdateBlocklistRunsOneAtATime(t *testing.T) {
	agent := &recordingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewDNSService(&config.Config{})
	svc.blocklistRunning.Store(true)
	if err := svc.UpdateBlocklist(context.Background()); !errors.Is(err, ErrBlocklistUpdateRunning) {
		t.Fatalf("second update: err = %v, want ErrBlocklistUpdateRunning", err)
	}
	if len(agent.calls) != 0 {
		t.Fatalf("a refused update ran %v", agent.calls)
	}

	svc.blocklistRunning.Store(false)
	if err := svc.UpdateBlocklist(context.Background()); err != nil {
		t.Fatalf("update: %v", err)
	}
	if svc.blocklistRunning.Load() {
		t.Error("the guard is still held after the update returned")
	}
}
