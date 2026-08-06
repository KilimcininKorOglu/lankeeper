package services_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// localFakeAgent is a minimal stub for cross-service backup tests.
// It mirrors a write to the real filesystem so the BackupService
// Export path (which routes file ops through the agent) can be
// exercised against a TempDir.
type localFakeAgent struct {
	mu      sync.Mutex
	writes  []string
}

func (f *localFakeAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "exec.run":
		raw, _ := json.Marshal(params)
		var p struct {
			Cmd  string   `json:"cmd"`
			Args []string `json:"args"`
		}
		_ = json.Unmarshal(raw, &p)
		// tar c is the only command Export issues; honour it locally.
		if p.Cmd == "tar" {
			// Synthesise a tarball locally rather than running real tar
			// across /etc paths the test process cannot read. Args[1]
			// is the output file (after "czf").
			if len(p.Args) >= 2 && p.Args[0] == "czf" {
				_ = os.WriteFile(p.Args[1], []byte("fake-tar-payload"), 0o600)
			}
			return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
		}
		return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
	case "file.write":
		raw, _ := json.Marshal(params)
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(raw, &p)
		if dir := filepath.Dir(p.Path); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		_ = os.WriteFile(p.Path, []byte(p.Content), 0o644)
		f.writes = append(f.writes, p.Path)
		return []byte(`{}`), nil
	case "file.mkdir":
		raw, _ := json.Marshal(params)
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &p)
		_ = os.MkdirAll(p.Path, 0o755)
		return []byte(`{}`), nil
	case "file.read":
		raw, _ := json.Marshal(params)
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &p)
		body, err := os.ReadFile(p.Path)
		if err != nil {
			return nil, err
		}
		out, _ := json.Marshal(struct {
			Content string `json:"content"`
		}{Content: string(body)})
		return out, nil
	}
	return nil, fmt.Errorf("unhandled %s", method)
}

// TestBackupOrchestratorRunsAndRotates wires a BackupService and
// orchestrator against a TempDir and verifies the full cycle:
// encrypted export, local upload, retention rollover, history
// recorded, lastRun updated. SFTP/S3 paths are exercised by
// per-target unit tests; this is the integration-level safety net
// for the local + history persistence path.
func TestBackupOrchestratorRunsAndRotates(t *testing.T) {
	// Process-global agent client; cannot run in parallel.
	agent := &localFakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfgDir := t.TempDir()
	backupDir := filepath.Join(cfgDir, "backups")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed something for tar to find.
	if err := os.WriteFile(filepath.Join(cfgDir, "router.yaml"), []byte("system:\n  hostname: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	services.SetBackupRootForTesting(backupDir + "/")
	t.Cleanup(func() { services.SetBackupRootForTesting("/var/lib/lankeeper/backups/") })

	cfg := &config.Config{}
	cfgPath := filepath.Join(cfgDir, "router.yaml")
	cfg.SetFilePath(cfgPath)
	cfg.Backup = config.BackupConfig{
		Enabled:    true,
		Schedule:   "@daily",
		Passphrase: "test-passphrase",
		Retention:  2,
		Targets: []config.BackupTarget{
			{Type: "local", Name: "local-test", Path: backupDir},
		},
	}

	svc := services.NewBackupService(cfgDir)
	orch := services.NewBackupOrchestrator(svc, cfg)
	_ = orch.SnapshotProvider()

	// First run.
	if err := svc.RunNow(context.Background()); err != nil {
		t.Fatalf("RunNow #1: %v", err)
	}
	// Second run.
	if err := svc.RunNow(context.Background()); err != nil {
		t.Fatalf("RunNow #2: %v", err)
	}
	// Third run - retention=2 should drop the oldest.
	if err := svc.RunNow(context.Background()); err != nil {
		t.Fatalf("RunNow #3: %v", err)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "lankeeper-backup-") && !strings.HasSuffix(e.Name(), ".tmp") {
			count++
		}
	}
	if count > 2 {
		t.Errorf("after retention=2: %d files, want <=2", count)
	}

	if len(cfg.Backup.History) != 3 {
		t.Errorf("history len = %d, want 3", len(cfg.Backup.History))
	}
	if cfg.Backup.LastStatus != "ok" {
		t.Errorf("LastStatus = %q, want ok", cfg.Backup.LastStatus)
	}
	if cfg.Backup.LastRun.IsZero() {
		t.Error("LastRun not set")
	}
}

// assertRunRecordedFailure checks the operator-visible trace a failed
// run must leave. The Backup page renders cfg.Backup.History, and the
// lankeeper_backup_last_run/last_status gauges read LastRun and
// LastStatus, so a run that aborts without touching these leaves both
// surfaces frozen on the previous success.
func assertRunRecordedFailure(t *testing.T, cfg *config.Config, wantMessage string) {
	t.Helper()

	if len(cfg.Backup.History) != 1 {
		t.Fatalf("history has %d entries, want 1: the aborted run left no trace",
			len(cfg.Backup.History))
	}
	entry := cfg.Backup.History[0]
	if entry.Status != "error" {
		t.Errorf("history status = %q, want %q", entry.Status, "error")
	}
	if !strings.Contains(entry.Message, wantMessage) {
		t.Errorf("history message %q does not mention %q", entry.Message, wantMessage)
	}
	if cfg.Backup.LastStatus != "error" {
		t.Errorf("LastStatus = %q, want %q; the metrics gauge reads this field",
			cfg.Backup.LastStatus, "error")
	}
	if cfg.Backup.LastRun.IsZero() {
		t.Error("LastRun not set, so the last-run gauge keeps reporting the previous success")
	}
	if !strings.Contains(cfg.Backup.LastError, wantMessage) {
		t.Errorf("LastError %q does not mention %q", cfg.Backup.LastError, wantMessage)
	}
}

func TestBackupOrchestratorRequiresPassphrase(t *testing.T) {
	cfgDir := t.TempDir()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(cfgDir, "router.yaml"))
	cfg.Backup = config.BackupConfig{
		Enabled:   true,
		Targets:   []config.BackupTarget{{Type: "local", Name: "x"}},
		Retention: 1,
	}
	svc := services.NewBackupService(cfgDir)
	services.NewBackupOrchestrator(svc, cfg)
	err := svc.RunNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("expected passphrase error, got %v", err)
	}
	assertRunRecordedFailure(t, cfg, "passphrase")
}

func TestBackupOrchestratorRequiresTargets(t *testing.T) {
	cfgDir := t.TempDir()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(cfgDir, "router.yaml"))
	cfg.Backup = config.BackupConfig{
		Enabled:    true,
		Passphrase: "x",
		Retention:  1,
	}
	svc := services.NewBackupService(cfgDir)
	services.NewBackupOrchestrator(svc, cfg)
	err := svc.RunNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no backup targets") {
		t.Errorf("expected targets error, got %v", err)
	}
	assertRunRecordedFailure(t, cfg, "no backup targets")
}

// TestBackupOrchestratorRecordsFailureToDisk covers the persistence
// half: recordHistory writes through cfg.SaveToFile, so the trace has
// to survive a restart, not just live in memory.
func TestBackupOrchestratorRecordsFailureToDisk(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "router.yaml")
	cfg := &config.Config{}
	cfg.SetFilePath(cfgPath)
	cfg.Backup = config.BackupConfig{
		Enabled:   true,
		Targets:   []config.BackupTarget{{Type: "local", Name: "x"}},
		Retention: 1,
	}
	svc := services.NewBackupService(cfgDir)
	services.NewBackupOrchestrator(svc, cfg)

	if err := svc.RunNow(context.Background()); err == nil {
		t.Fatal("expected the run to fail")
	}

	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(reloaded.Backup.History) != 1 {
		t.Fatalf("persisted history has %d entries, want 1", len(reloaded.Backup.History))
	}
	if reloaded.Backup.LastStatus != "error" {
		t.Errorf("persisted LastStatus = %q, want %q", reloaded.Backup.LastStatus, "error")
	}
}
