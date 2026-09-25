package services

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// stagingAgent models the root agent across the systemd sandbox for an
// export: tar and chmod act on the real filesystem, but only inside the
// visible directories. The web process's private /tmp is not among
// them, so the agent cannot create or change a file there.
type stagingAgent struct {
	mu      sync.Mutex
	visible []string
	calls   []string
}

func (a *stagingAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
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

	path := p.Args[len(p.Args)-1]
	if p.Cmd == "tar" {
		path = p.Args[1]
	}
	if !a.sees(path) {
		return nil, fmt.Errorf("%s: %s: No such file or directory", p.Cmd, path)
	}
	if err := a.apply(p.Cmd, p.Args); err != nil {
		return nil, err
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

// apply runs tar (as a gzip stream holding a known secret), chmod and rm
// against the real file.
func (a *stagingAgent) apply(cmd string, args []string) error {
	switch cmd {
	case "tar":
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write([]byte("sessionSecret: keep-me\n")); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		return os.WriteFile(args[1], buf.Bytes(), 0o644)
	case "chmod":
		var mode uint32
		if _, err := fmt.Sscanf(args[0], "%o", &mode); err != nil {
			return err
		}
		return os.Chmod(args[1], os.FileMode(mode))
	case "rm":
		return os.Remove(args[len(args)-1])
	}
	return nil
}

func (a *stagingAgent) sees(path string) bool {
	for _, dir := range a.visible {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// useBackupStaging points the data directory at a temp dir and creates
// the staging directory the installer provides, returning both.
func useBackupStaging(t *testing.T) (dataDir, staging string) {
	t.Helper()
	dataDir = t.TempDir()
	t.Setenv("LANKEEPER_DATA_DIR", dataDir)
	staging = filepath.Join(dataDir, "staging")
	if err := os.Mkdir(staging, 0o750); err != nil {
		t.Fatalf("create staging dir: %v", err)
	}
	return dataDir, staging
}

// TestEncryptedExportCrossesTheSandbox is the regression test. The agent
// ran tar into the caller's path under the web process's /tmp, and the
// web process then read it back, but the web unit runs with PrivateTmp,
// so the two never saw the same file. Even in a shared directory the
// archive tar created is root's, and the unprivileged process cannot
// replace it with the encrypted copy.
func TestEncryptedExportCrossesTheSandbox(t *testing.T) {
	dataDir, staging := useBackupStaging(t)
	agent := &stagingAgent{visible: []string{dataDir}}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	// The caller's output path is in the web process's own temp space,
	// which the agent cannot see.
	out := filepath.Join(t.TempDir(), "lankeeper-backup.tar.gz.enc")
	if err := NewBackupService(t.TempDir()).Export(context.Background(), out, "passphrase"); err != nil {
		t.Fatalf("export: %v (agent calls: %q)", err, agent.calls)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if _, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
		t.Error("the output still parses as gzip, so it was not encrypted")
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("output mode = %v, %v; want 600", info.Mode().Perm(), err)
	}
	if left, _ := os.ReadDir(staging); len(left) != 0 {
		t.Errorf("the plaintext archive was left in staging: %v", left)
	}
}
