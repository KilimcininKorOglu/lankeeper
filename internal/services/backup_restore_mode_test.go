package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// modeRecordingAgent applies the requested mode instead of a fixed one,
// so the mode the restore asks for is observable on disk. The shared
// restoreFakeAgent hardcodes 0600, which would pass this test whatever
// the service requested.
type modeRecordingAgent struct {
	mu    sync.Mutex
	modes map[string]os.FileMode
}

func (a *modeRecordingAgent) record(path string, mode os.FileMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.modes == nil {
		a.modes = make(map[string]os.FileMode)
	}
	a.modes[path] = mode
}

func (a *modeRecordingAgent) requested(path string) (os.FileMode, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m, ok := a.modes[path]
	return m, ok
}

func (a *modeRecordingAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	switch method {
	case "file.mkdir":
		var p struct {
			Path string `json:"path"`
			Mode int    `json:"mode"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		a.record(p.Path, os.FileMode(p.Mode))
		if err := os.MkdirAll(p.Path, os.FileMode(p.Mode)); err != nil {
			return nil, err
		}
	case "file.write":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Mode    int    `json:"mode"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		a.record(p.Path, os.FileMode(p.Mode))
		if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p.Path, []byte(p.Content), os.FileMode(p.Mode)); err != nil {
			return nil, err
		}
	}
	return []byte(`{"status":"ok"}`), nil
}

// writeArchiveWithModes builds a gzipped tar whose members carry the
// modes a tampered archive would.
func writeArchiveWithModes(t *testing.T, path string, files map[string]int64, dirs map[string]int64) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, mode := range dirs {
		hdr := &tar.Header{Name: name, Mode: mode, Typeflag: tar.TypeDir}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar dir header %s: %v", name, err)
		}
	}
	for name, mode := range files {
		const body = "secret\n"
		hdr := &tar.Header{
			Name:     name,
			Mode:     mode,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
}

// TestImportIgnoresTheArchiveFileMode is the regression test. Import
// applied os.FileMode(hdr.Mode) verbatim, so an archive edited by anyone
// who could reach it, on the operator's machine or in remote storage,
// decided the permissions of the restored router.yaml. That file holds
// the session secret and the admin password hash, and the normal save
// path writes it 0600.
//
// Mutates the process-global agent client, so no t.Parallel here.
func TestImportIgnoresTheArchiveFileMode(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")

	origExtra := backupExtraDirs
	backupExtraDirs = nil
	t.Cleanup(func() { backupExtraDirs = origExtra })

	fake := &modeRecordingAgent{}
	netutil.SetAgentClient(fake)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	archive := filepath.Join(root, "backup.tar.gz")
	writeArchiveWithModes(t, archive,
		map[string]int64{"lankeeper/router.yaml": 0o666},
		nil,
	)

	svc := NewBackupService(cfgDir)
	if err := svc.Import(context.Background(), archive, ""); err != nil {
		t.Fatalf("import: %v", err)
	}

	target := filepath.Join(cfgDir, "router.yaml")
	got, ok := fake.requested(target)
	if !ok {
		t.Fatalf("router.yaml was never written")
	}
	if got != restoreFileMode {
		t.Errorf("requested mode = %#o, want %#o", got, restoreFileMode)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat restored file: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("restored router.yaml is reachable by others: %#o", info.Mode().Perm())
	}
}

// TestImportIgnoresTheArchiveDirectoryMode covers the directory half.
// The old expression OR-ed 0755 onto the header mode, which could only
// widen it, so 0777 in the archive produced 0777 on disk.
func TestImportIgnoresTheArchiveDirectoryMode(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")

	origExtra := backupExtraDirs
	backupExtraDirs = nil
	t.Cleanup(func() { backupExtraDirs = origExtra })

	fake := &modeRecordingAgent{}
	netutil.SetAgentClient(fake)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	archive := filepath.Join(root, "backup.tar.gz")
	writeArchiveWithModes(t, archive, nil, map[string]int64{"lankeeper/certs": 0o777})

	svc := NewBackupService(cfgDir)
	if err := svc.Import(context.Background(), archive, ""); err != nil {
		t.Fatalf("import: %v", err)
	}

	target := filepath.Join(cfgDir, "certs")
	got, ok := fake.requested(target)
	if !ok {
		t.Fatalf("the directory was never created")
	}
	if got != restoreDirMode {
		t.Errorf("requested mode = %#o, want %#o", got, restoreDirMode)
	}
	if got&0o022 != 0 {
		t.Errorf("restored directory is group- or world-writable: %#o", got)
	}
}

// TestRestoredModesAreAcceptedByTheAgent ties the two halves together:
// the policy Import applies must be one the agent will actually honour,
// or a restore would fail on every member.
func TestRestoredModesAreAcceptedByTheAgent(t *testing.T) {
	for _, mode := range []os.FileMode{restoreFileMode, restoreDirMode} {
		if mode&^os.FileMode(0o777) != 0 {
			t.Errorf("%#o carries bits outside the permission set", mode)
		}
		if mode&0o022 != 0 {
			t.Errorf("%#o is group- or world-writable", mode)
		}
	}
}
