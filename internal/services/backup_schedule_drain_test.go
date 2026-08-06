package services

import (
	"context"
	"sync"
	"testing"
	"time"
)

// resetSchedulerFlag clears the package-level guard so a test can start
// the scheduler. The flag is global, so these tests never run in
// parallel with each other.
func resetSchedulerFlag(t *testing.T) {
	t.Helper()
	scheduleMu.Lock()
	schedulerRunning = false
	scheduleMu.Unlock()
	t.Cleanup(func() {
		scheduleMu.Lock()
		schedulerRunning = false
		scheduleMu.Unlock()
	})
}

func idleSchedulerConfig() *backupSchedulerConfig {
	return &backupSchedulerConfig{provider: func() backupSnapshot {
		return backupSnapshot{Enabled: false}
	}}
}

// TestStartSchedulerIsCountedIntoWaitGroup is the regression test.
// Shutdown had no way to wait for the scheduler, and because RunNow is
// called synchronously inside its loop, exiting during a run skipped
// the history entry entirely and left the archive in the temp dir.
//
// Wait returning after cancellation is what proves the goroutine is
// tracked; before the fix nothing incremented the counter, so Wait
// returned immediately whether or not the goroutine had stopped.
func TestStartSchedulerIsCountedIntoWaitGroup(t *testing.T) {
	resetSchedulerFlag(t)

	svc := NewBackupService(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	svc.StartScheduler(ctx, idleSchedulerConfig(), &wg)

	// The counter must be held while the goroutine runs, so a Wait
	// started now must not return until cancellation.
	waited := make(chan struct{})
	go func() {
		wg.Wait()
		close(waited)
	}()

	select {
	case <-waited:
		t.Fatal("Wait returned while the scheduler was still running, so it was never counted")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler goroutine did not drain after cancellation")
	}

	// The goroutine clears the running flag before it returns, so a
	// returned Wait must never leave it set.
	scheduleMu.Lock()
	running := schedulerRunning
	scheduleMu.Unlock()
	if running {
		t.Error("Wait returned while schedulerRunning was still set")
	}
}

// TestStartSchedulerAcceptsNilWaitGroup keeps the parameter optional
// for callers that do not drain.
func TestStartSchedulerAcceptsNilWaitGroup(t *testing.T) {
	resetSchedulerFlag(t)

	svc := NewBackupService(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc.StartScheduler(ctx, idleSchedulerConfig(), nil)
	cancel()

	// Give the goroutine a moment to exit; a nil dereference would
	// panic and fail the test through the runtime.
	time.Sleep(50 * time.Millisecond)
}
