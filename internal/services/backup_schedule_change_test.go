package services

import (
	"context"
	"testing"
	"time"
)

// An edited schedule must replace the fire time computed from the old
// expression, not wait for the old slot to fire first.
func TestSchedulerTickRecomputesWhenTheScheduleChanges(t *testing.T) {
	snap := backupSnapshot{Enabled: true, Schedule: "0 3 1 * *", Location: time.UTC}
	cfg := &backupSchedulerConfig{provider: func() backupSnapshot { return snap }}
	svc := &BackupService{}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	next := svc.schedulerTick(context.Background(), cfg, now, scheduledFire{})
	if want := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC); !next.at.Equal(want) {
		t.Fatalf("monthly next = %v, want %v", next.at, want)
	}

	snap.Schedule = "0 3 * * *"
	next = svc.schedulerTick(context.Background(), cfg, now.Add(30*time.Second), next)
	if want := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC); !next.at.Equal(want) {
		t.Fatalf("after switching to daily, next = %v, want %v", next.at, want)
	}
}
