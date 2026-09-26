package services

import (
	"context"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// Each source syncs on its own schedule, and only that source: the old
// loop ignored the expression and synced every source per scheduled one.
func TestM3USourcesSyncOnTheirOwnSchedule(t *testing.T) {
	cfg := config.DefaultConfig()
	// Paths outside /srv and /mnt fail validation before any download,
	// so each synced source shows up as exactly one error.
	cfg.NAS.M3USources = []config.M3USourceConfig{
		{URL: "https://example.com/a.m3u", DownloadPath: "/etc/a", Schedule: "0 4 * * *"},
		{URL: "https://example.com/b.m3u", DownloadPath: "/etc/b"},
	}
	svc := NewNASService(cfg)
	next := map[string]time.Time{}

	svc.m3uScheduleTick(context.Background(), time.Date(2026, 9, 26, 3, 59, 0, 0, time.Local), next)
	if !svc.GetM3UStatus().LastSync.IsZero() {
		t.Fatal("a source synced before its schedule was due")
	}
	svc.m3uScheduleTick(context.Background(), time.Date(2026, 9, 26, 4, 0, 0, 0, time.Local), next)
	status := svc.GetM3UStatus()
	if status.LastSync.IsZero() {
		t.Fatal("the scheduled source did not sync when due")
	}
	if status.Errors != 1 {
		t.Errorf("errors = %d, want 1: only the scheduled source may sync", status.Errors)
	}
}
