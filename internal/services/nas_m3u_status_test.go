package services

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// A second sync while one runs would write the same .strm files and
// interleave the counts the page shows.
func TestM3USyncRefusesToOverlap(t *testing.T) {
	svc := NewNASService(config.DefaultConfig())
	svc.m3uStatus.Running = true
	if err := svc.SyncM3U(context.Background()); !errors.Is(err, ErrM3USyncRunning) {
		t.Fatalf("overlapping sync: err = %v, want ErrM3USyncRunning", err)
	}
}

// The sync goroutine writes the status while page requests read it; run
// under -race, this fails when either side skips the lock.
func TestM3UStatusIsReadUnderTheLock(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.NAS.M3USources = nil
	svc := NewNASService(cfg)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			_ = svc.SyncM3U(context.Background())
		}
	})
	wg.Go(func() {
		for range 50 {
			_ = svc.GetM3UStatus()
		}
	})
	wg.Wait()
	if svc.GetM3UStatus().Running {
		t.Error("the status still reports a running sync")
	}
}
