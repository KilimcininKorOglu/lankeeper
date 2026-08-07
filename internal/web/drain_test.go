package web

import (
	"sync"
	"testing"
	"time"
)

// TestDrainBackgroundWaitsForWork covers the shutdown path: an
// in-flight backup must be allowed to record its history entry before
// the process exits.
func TestDrainBackgroundWaitsForWork(t *testing.T) {
	var wg sync.WaitGroup
	finished := make(chan struct{})

	wg.Go(func() {
		time.Sleep(150 * time.Millisecond)
		close(finished)
	})

	drainBackground(&wg, 5*time.Second)

	select {
	case <-finished:
	default:
		t.Error("drain returned before the background work finished")
	}
}

// TestDrainBackgroundGivesUpAtTheTimeout keeps the drain from handing
// the process to systemd's kill timer. The web unit sets no
// TimeoutStopSec, so an unbounded wait would end in SIGKILL, which is
// the outcome the drain exists to avoid.
func TestDrainBackgroundGivesUpAtTheTimeout(t *testing.T) {
	var wg sync.WaitGroup
	release := make(chan struct{})

	wg.Go(func() {
		<-release
	})
	t.Cleanup(func() {
		close(release)
		wg.Wait()
	})

	start := time.Now()
	drainBackground(&wg, 100*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 100*time.Millisecond {
		t.Errorf("drain returned after %v, before its own timeout", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("drain blocked for %v despite a 100ms timeout", elapsed)
	}
}
