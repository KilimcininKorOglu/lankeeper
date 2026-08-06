package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// BackupOrchestrator wires the BackupService runtime callbacks
// against a live config so the scheduler goroutine and the manual
// "Run Now" handler share a single code path.
type BackupOrchestrator struct {
	svc  *BackupService
	cfg  *config.Config
	save func() error // typically cfg.SaveToFile
}

// NewBackupOrchestrator installs the runner callback on the service
// and returns a snapshot provider for StartScheduler.
func NewBackupOrchestrator(svc *BackupService, cfg *config.Config) *BackupOrchestrator {
	o := &BackupOrchestrator{svc: svc, cfg: cfg, save: cfg.SaveToFile}
	svc.SetRunner(o.runOnce)
	return o
}

// SnapshotProvider returns the live config slice the scheduler uses.
// Wrapped through this method so we can inject the active timezone
// loaded from cfg.System.Timezone.
func (o *BackupOrchestrator) SnapshotProvider() *backupSchedulerConfig {
	return &backupSchedulerConfig{provider: func() backupSnapshot {
		loc := time.Local
		if tz := o.cfg.System.Timezone; tz != "" {
			if l, err := time.LoadLocation(tz); err == nil {
				loc = l
			}
		}
		return backupSnapshot{
			Enabled:  o.cfg.Backup.Enabled,
			Schedule: o.cfg.Backup.Schedule,
			Location: loc,
			LastRun:  o.cfg.Backup.LastRun,
		}
	}}
}

// runOnce executes one backup cycle: encrypted export to /tmp,
// per-target upload + retention, history record + persist. Wrapped
// in svc.runMu so the scheduler and a manual click can't collide.
//
// We never abort on a single target failure; per-target errors
// surface in the returned message and the per-target error counter,
// but the run continues so a flaky S3 endpoint doesn't deny the
// operator a known-good local snapshot.
func (o *BackupOrchestrator) runOnce(ctx context.Context) error {
	o.svc.runMu.Lock()
	defer o.svc.runMu.Unlock()

	started := time.Now()
	bcfg := o.cfg.Backup

	if bcfg.Passphrase == "" {
		return o.failRun(started, errors.New("backup passphrase not configured"))
	}
	if len(bcfg.Targets) == 0 {
		return o.failRun(started, errors.New("no backup targets configured"))
	}
	retention := bcfg.Retention
	if retention < 1 {
		retention = 7
	}

	stamp := started.Format("20060102-150405")
	tmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("lankeeper-backup-%s.tar.gz.enc", stamp))
	defer func() { _ = os.Remove(tmpPath) }()

	if err := o.svc.Export(ctx, tmpPath, bcfg.Passphrase); err != nil {
		return o.failRun(started, fmt.Errorf("export: %w", err))
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return o.failRun(started, fmt.Errorf("stat archive: %w", err))
	}
	size := info.Size()

	var (
		successTargets []string
		errMessages    []string
	)
	for _, target := range bcfg.Targets {
		if err := o.uploadOne(ctx, tmpPath, target, retention); err != nil {
			errMessages = append(errMessages, fmt.Sprintf("%s: %v", target.Name, err))
			log.Printf("backup: target %s failed: %v", target.Name, err)
			continue
		}
		successTargets = append(successTargets, target.Name)
	}

	status := "ok"
	switch {
	case len(successTargets) == 0:
		status = "error"
	case len(errMessages) > 0:
		status = "partial"
	}

	entry := historyEntry{
		StartedAt:   started,
		CompletedAt: time.Now(),
		Bytes:       size,
		Targets:     successTargets,
		Status:      status,
		Message:     strings.Join(errMessages, "; "),
	}
	o.recordHistory(entry)

	if status == "error" {
		return fmt.Errorf("all targets failed: %s", entry.Message)
	}
	return nil
}

// uploadOne dispatches by target type and triggers per-target
// retention immediately after a successful upload. Per-target
// retention rather than global so a fragile remote doesn't drag
// down healthy ones.
func (o *BackupOrchestrator) uploadOne(ctx context.Context, src string, t config.BackupTarget, keep int) error {
	switch t.Type {
	case "local":
		if _, err := uploadLocal(ctx, src, t); err != nil {
			return err
		}
		if _, err := cleanupLocal(t, keep); err != nil {
			log.Printf("backup: cleanup local %s: %v", t.Name, err)
		}
	case "s3":
		if _, err := uploadS3(ctx, src, t); err != nil {
			return err
		}
		if _, err := cleanupS3(ctx, t, keep); err != nil {
			log.Printf("backup: cleanup s3 %s: %v", t.Name, err)
		}
	case "sftp":
		if _, err := uploadSFTP(ctx, src, t); err != nil {
			return err
		}
		if _, err := cleanupSFTP(ctx, t, keep); err != nil {
			log.Printf("backup: cleanup sftp %s: %v", t.Name, err)
		}
	default:
		return fmt.Errorf("unknown target type %q", t.Type)
	}
	return nil
}

// failRun records a failed run before returning its error.
//
// Every abort has to leave a trace, including the ones that fire before
// any work starts. The Backup page and the LastRun/LastStatus gauges
// read only from the history the run writes, so a bare return froze
// both on the last success while the scheduler kept retrying a config
// that could not work.
//
// The error is returned unchanged so wrapping survives for callers.
func (o *BackupOrchestrator) failRun(started time.Time, err error) error {
	o.recordHistory(historyEntry{
		StartedAt:   started,
		CompletedAt: time.Now(),
		Status:      "error",
		Message:     err.Error(),
	})
	return err
}

// recordHistory persists the run entry to BackupConfig.History
// and updates LastRun/LastStatus/LastError. We trim to the ring
// buffer cap and persist via cfg.SaveToFile so a crash before the
// next run preserves the audit trail.
func (o *BackupOrchestrator) recordHistory(entry historyEntry) {
	cfgEntry := config.BackupHistory{
		StartedAt:   entry.StartedAt,
		CompletedAt: entry.CompletedAt,
		Bytes:       entry.Bytes,
		Targets:     entry.Targets,
		Status:      entry.Status,
		Message:     entry.Message,
	}
	hist := append(o.cfg.Backup.History, cfgEntry)
	if len(hist) > MaxBackupHistory {
		hist = hist[len(hist)-MaxBackupHistory:]
	}
	o.cfg.Backup.History = hist
	o.cfg.Backup.LastRun = entry.StartedAt
	o.cfg.Backup.LastStatus = entry.Status
	o.cfg.Backup.LastError = entry.Message
	if err := o.save(); err != nil {
		log.Printf("backup: save history: %v", err)
	}
}
