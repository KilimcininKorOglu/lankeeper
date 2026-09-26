package services

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// The history is persisted in router.yaml on every run, so the cap has
// to hold on the path production takes, recordHistory.
func TestBackupHistoryStaysCapped(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	o := NewBackupOrchestrator(NewBackupService(t.TempDir()), cfg)

	for i := range MaxBackupHistory + 5 {
		o.recordHistory(historyEntry{StartedAt: time.Unix(int64(i), 0), Status: "ok"})
	}
	hist := cfg.Backup.History
	if len(hist) != MaxBackupHistory {
		t.Fatalf("history length = %d, want %d", len(hist), MaxBackupHistory)
	}
	if hist[0].StartedAt.Unix() != 5 || hist[len(hist)-1].StartedAt.Unix() != int64(MaxBackupHistory+4) {
		t.Errorf("kept %d..%d, want the newest %d runs", hist[0].StartedAt.Unix(), hist[len(hist)-1].StartedAt.Unix(), MaxBackupHistory)
	}
}
