package services

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// The history is rendered on the backup page and saved in router.yaml,
// so root stderr carried by an agent error must not land in it.
func TestFailRunKeepsAgentDetailOutOfTheHistory(t *testing.T) {
	cfg := &config.Config{}
	o := &BackupOrchestrator{cfg: cfg, save: func() error { return nil }}

	agentErr := &netutil.AgentError{Op: "exec.run", Target: "tar",
		Err: errors.New("exec tar: exit status 2 (stderr: tar: /etc/secret-path: Permission denied)")}
	_ = o.failRun(time.Now(), fmt.Errorf("export: %w", agentErr))

	got := cfg.Backup.History[len(cfg.Backup.History)-1].Message
	for _, leak := range []string{"stderr", "/etc/secret-path", "tar"} {
		if strings.Contains(got, leak) || strings.Contains(cfg.Backup.LastError, leak) {
			t.Fatalf("history message %q leaks %q", got, leak)
		}
	}
}

func TestFailRunKeepsOwnValidationText(t *testing.T) {
	cfg := &config.Config{}
	o := &BackupOrchestrator{cfg: cfg, save: func() error { return nil }}
	_ = o.failRun(time.Now(), errors.New("no backup targets configured"))

	if got := cfg.Backup.LastError; got != "no backup targets configured" {
		t.Fatalf("LastError = %q, want the validation text", got)
	}
}
