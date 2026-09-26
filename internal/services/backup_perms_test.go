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

// exportAgent honours file.write, rm, tar and chmod against the real
// filesystem so the
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
	if method == "file.write" {
		return fakeFileWrite(params)
	}
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
		err = a.fakeTar(p.Args)
	case "chmod":
		err = fakeChmod(p.Args)
	case "rm":
		err = os.Remove(p.Args[len(p.Args)-1])
		if os.IsNotExist(err) {
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

// fakeFileWrite creates the named file with the requested mode.
func fakeFileWrite(params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Mode    int    `json:"mode"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p.Path, []byte(p.Content), os.FileMode(p.Mode)); err != nil {
		return nil, err
	}
	return []byte(`{}`), os.Chmod(p.Path, os.FileMode(p.Mode))
}

// fakeTar writes the archive a czf invocation names. Like GNU tar it
// truncates an existing file and keeps its mode, and creates a missing
// one with tarMode.
func (a *exportAgent) fakeTar(args []string) error {
	if len(args) < 2 || args[0] != "czf" {
		return nil
	}
	if _, err := os.Stat(args[1]); err == nil {
		return os.WriteFile(args[1], []byte("archive"), 0)
	}
	if err := os.WriteFile(args[1], []byte("archive"), a.tarMode); err != nil {
		return err
	}
	// WriteFile applies the umask, so set the mode explicitly.
	return os.Chmod(args[1], a.tarMode)
}

// fakeChmod applies an octal "chmod MODE PATH" to the real file.
func fakeChmod(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("chmod: unexpected args %v", args)
	}
	var mode uint32
	if _, err := fmt.Sscanf(args[0], "%o", &mode); err != nil {
		return fmt.Errorf("chmod: bad mode %q", args[0])
	}
	return os.Chmod(args[1], os.FileMode(mode))
}

// useBackupStagingDir points the data directory at a temp dir holding
// the staging directory the installer creates.
func useBackupStagingDir(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("LANKEEPER_DATA_DIR", dataDir)
	if err := os.Mkdir(filepath.Join(dataDir, "staging"), 0o750); err != nil {
		t.Fatalf("create staging dir: %v", err)
	}
}

// TestExportRestrictsAnUnencryptedArchive is the regression test. The
// plain branch once left the archive with tar's mode, so the one
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
}

// TestExportRestrictsAnEncryptedArchive keeps the passphrase path at the
// same mode, so the guard does not depend on which branch ran.
func TestExportRestrictsAnEncryptedArchive(t *testing.T) {
	useBackupStagingDir(t)
	// No credential key: the fake tar writes no real archive to append it to.
	t.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(t.TempDir(), "absent.key"))
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
