package services

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// The backup handlers wrote cfg.Backup with no lock while the history
// writer, the scheduler and /metrics touched the same fields. Run under
// -race, this fails when any of them skips the orchestrator lock; two
// concurrent adds of one name must also leave a single target.
func TestBackupConfigAccessIsSerialized(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(dir, "router.yaml"))
	svc := NewBackupService(dir)
	o := NewBackupOrchestrator(svc, cfg)
	metrics := NewMetricsService(cfg, nil, nil, nil, nil, nil, svc, nil, nil)

	var wg sync.WaitGroup
	var dup sync.WaitGroup
	for range 2 {
		dup.Go(func() {
			err := o.AddTarget(config.BackupTarget{Name: "same", Type: "local"})
			if err != nil && !errors.Is(err, ErrBackupTargetExists) {
				t.Errorf("add: %v", err)
			}
		})
	}
	wg.Go(func() {
		for i := range 30 {
			name := fmt.Sprintf("t%d", i)
			if err := o.AddTarget(config.BackupTarget{Name: name, Type: "local"}); err != nil {
				t.Errorf("add %s: %v", name, err)
			}
			o.recordHistory(historyEntry{StartedAt: time.Now(), Status: "ok"})
			if err := o.RemoveTarget(name); err != nil {
				t.Errorf("remove %s: %v", name, err)
			}
		}
	})
	wg.Go(func() {
		for range 30 {
			_ = o.Settings()
			_ = o.SnapshotProvider().provider()
			var snap MetricsSnapshot
			metrics.collectBackup(&snap)
		}
	})
	wg.Wait()
	dup.Wait()

	if n := len(o.Settings().Targets); n != 1 {
		t.Errorf("%d targets remain, want the single %q", n, "same")
	}
}
