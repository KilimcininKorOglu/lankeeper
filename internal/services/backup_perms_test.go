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

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// exportAgent honours tar and chmod against the real filesystem so the
// mode the archive ends up with can be asserted, and records every
// command for the ordering checks.
type exportAgent struct {
	mu    sync.Mutex
	calls []string
	// tarMode is the mode the synthesised archive is created with,
	// standing in for what a root tar under the default umask produces.
	tarMode os.FileMode
}

func (a *exportAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return []byte(`{}`), nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.calls = append(a.calls, p.Cmd+" "+strings.Join(p.Args, " "))
	a.mu.Unlock()

	switch p.Cmd {
	case "tar":
		if len(p.Args) >= 2 && p.Args[0] == "czf" {
			if err := os.WriteFile(p.Args[1], []byte("archive"), a.tarMode); err != nil {
				return nil, err
			}
			// WriteFile applies the umask, so set the mode explicitly.
			if err := os.Chmod(p.Args[1], a.tarMode); err != nil {
				return nil, err
			}
		}
	case "chmod":
		if len(p.Args) != 2 {
			return nil, fmt.Errorf("chmod: unexpected args %v", p.Args)
		}
		var mode uint32
		if _, err := fmt.Sscanf(p.Args[0], "%o", &mode); err != nil {
			return nil, fmt.Errorf("chmod: bad mode %q", p.Args[0])
		}
		if err := os.Chmod(p.Args[1], os.FileMode(mode)); err != nil {
			return nil, err
		}
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func (a *exportAgent) snapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// TestExportRestrictsAnUnencryptedArchive is the regression test. The
// only chmod in Export lived inside the encryption branch, so the one
// caller that passes no passphrase, the pre-update snapshot, left a
// world-readable archive holding router.yaml: the session secret, the
// admin password hash, the PPPoE password and every backup credential.
//
// Mutates the process-global agent client, so no t.Parallel here.
func TestExportRestrictsAnUnencryptedArchive(t *testing.T) {
	agent := &exportAgent{tarMode: 0o644}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	out := filepath.Join(t.TempDir(), "pre-update-v1.2.3.tar.gz")
	svc := services.NewBackupService(t.TempDir())

	if err := svc.Export(context.Background(), out, ""); err != nil {
		t.Fatalf("export: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("archive not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("archive mode = %o, want 600; it holds every secret on the device", got)
	}

	var sawChmod bool
	for _, c := range agent.snapshot() {
		if strings.HasPrefix(c, "chmod 600 ") {
			sawChmod = true
		}
	}
	if !sawChmod {
		t.Errorf("the archive was not restricted through the agent; calls: %v", agent.snapshot())
	}
}

// TestExportRestrictsAnEncryptedArchive keeps the passphrase path at the
// same mode, so the guard does not depend on which branch ran.
func TestExportRestrictsAnEncryptedArchive(t *testing.T) {
	agent := &exportAgent{tarMode: 0o644}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	out := filepath.Join(t.TempDir(), "backup.tar.gz")
	svc := services.NewBackupService(t.TempDir())

	if err := svc.Export(context.Background(), out, "passphrase"); err != nil {
		t.Fatalf("export: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("archive not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("encrypted archive mode = %o, want 600", got)
	}
}
