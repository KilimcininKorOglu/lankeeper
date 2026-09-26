package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// BackupOrchestrator wires the BackupService runtime callbacks
// against a live config so the scheduler goroutine and the manual
// "Run Now" handler share a single code path.
type BackupOrchestrator struct {
	svc  *BackupService
	cfg  *config.Config
	save func() error // typically cfg.SaveToFile

	// mu guards cfg.Backup for the page, the settings forms, the
	// scheduler, the history writer and /metrics. runMu serializes whole
	// runs and is held for minutes, so it cannot serve that role.
	mu sync.Mutex
}

var (
	ErrBackupTargetExists   = errors.New("a backup target with this name already exists")
	ErrBackupTargetNotFound = errors.New("backup target not found")
)

// NewBackupOrchestrator installs the runner callback on the service
// and returns a snapshot provider for StartScheduler.
func NewBackupOrchestrator(svc *BackupService, cfg *config.Config) *BackupOrchestrator {
	o := &BackupOrchestrator{svc: svc, cfg: cfg, save: cfg.SaveToFile}
	svc.SetRunner(o.runOnce)
	svc.settings = o.Settings
	return o
}

// Settings returns a copy of the backup config taken under o.mu.
func (o *BackupOrchestrator) Settings() config.BackupConfig {
	o.mu.Lock()
	defer o.mu.Unlock()
	b := o.cfg.Backup
	b.Targets = slices.Clone(b.Targets)
	b.History = slices.Clone(b.History)
	return b
}

// SaveSchedule stores the schedule settings and persists them. An empty
// passphrase keeps the stored one.
func (o *BackupOrchestrator) SaveSchedule(enabled bool, schedule string, retention int, passphrase string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cfg.Backup.Enabled = enabled
	o.cfg.Backup.Schedule = schedule
	o.cfg.Backup.Retention = retention
	if passphrase != "" {
		o.cfg.Backup.Passphrase = passphrase
	}
	return o.save()
}

// AddTarget appends a target whose name no other target holds. The
// check and the append are one critical section, so two submits cannot
// both pass the check.
func (o *BackupOrchestrator) AddTarget(target config.BackupTarget) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, t := range o.cfg.Backup.Targets {
		if t.Name == target.Name {
			return fmt.Errorf("%w: %s", ErrBackupTargetExists, target.Name)
		}
	}
	o.cfg.Backup.Targets = append(slices.Clip(o.cfg.Backup.Targets), target)
	return o.save()
}

// RemoveTarget deletes the named target.
func (o *BackupOrchestrator) RemoveTarget(name string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	i := slices.IndexFunc(o.cfg.Backup.Targets, func(t config.BackupTarget) bool { return t.Name == name })
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrBackupTargetNotFound, name)
	}
	o.cfg.Backup.Targets = slices.Delete(slices.Clone(o.cfg.Backup.Targets), i, i+1)
	return o.save()
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
		b := o.Settings()
		return backupSnapshot{
			Enabled:  b.Enabled,
			Schedule: b.Schedule,
			Location: loc,
			LastRun:  b.LastRun,
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
	bcfg := o.Settings()

	if bcfg.Passphrase == "" {
		return o.failRun(started, errors.New("backup passphrase not configured"))
	}
	if len(bcfg.Targets) == 0 {
		return o.failRun(started, errors.New("no backup targets configured"))
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

	successTargets, errMessages := o.uploadAll(ctx, tmpPath, bcfg.Targets, retentionOrDefault(bcfg.Retention))
	status := runStatus(len(successTargets), len(errMessages))

	entry := historyEntry{
		StartedAt:   started,
		CompletedAt: time.Now(),
		Bytes:       info.Size(),
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

// retentionOrDefault keeps seven archives per target unless a positive
// retention is configured.
func retentionOrDefault(retention int) int {
	if retention < 1 {
		return 7
	}
	return retention
}

// uploadAll sends the archive to every target and returns the names of
// the targets that took it and a message per target that failed.
func (o *BackupOrchestrator) uploadAll(ctx context.Context, src string, targets []config.BackupTarget, keep int) (succeeded, failures []string) {
	for _, target := range targets {
		if err := o.uploadOne(ctx, src, target, keep); err != nil {
			failures = append(failures, target.Name+": "+historyMessage(err))
			log.Printf("backup: target %s failed: %v", target.Name, err)
			continue
		}
		succeeded = append(succeeded, target.Name)
	}
	return succeeded, failures
}

// runStatus is "error" when no target succeeded, "partial" when some
// failed and "ok" otherwise.
func runStatus(succeeded, failed int) string {
	switch {
	case succeeded == 0:
		return "error"
	case failed > 0:
		return "partial"
	default:
		return "ok"
	}
}

// uploadOne dispatches by target type and triggers per-target
// retention immediately after a successful upload. Per-target
// retention rather than global so a fragile remote doesn't drag
// down healthy ones.
func (o *BackupOrchestrator) uploadOne(ctx context.Context, src string, t config.BackupTarget, keep int) error {
	switch t.Type {
	case "local":
		if _, err := uploadLocal(src, t); err != nil {
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
		Message:     historyMessage(err),
	})
	log.Printf("backup: run failed: %v", err)
	return err
}

// historyMessage is the text a run failure leaves in the history. The
// history is rendered on the backup page and persisted in router.yaml,
// and so in every later backup of it, so an error that crossed the agent
// boundary, which carries root stderr and internal paths, is replaced by
// a generic phrase. The full error goes to the journal.
func historyMessage(err error) string {
	if _, ok := errors.AsType[*netutil.AgentError](err); ok {
		return "privileged command failed, see the system journal"
	}
	return err.Error()
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
	o.mu.Lock()
	defer o.mu.Unlock()
	hist := append(slices.Clip(o.cfg.Backup.History), cfgEntry)
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
