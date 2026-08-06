package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// restoreFakeAgent mirrors file.write and file.mkdir onto the real
// filesystem so a restore can be exercised against temp directories.
// Import routes every write through the agent, so without this the
// destination path is never observable.
type restoreFakeAgent struct {
	mu sync.Mutex
}

func (f *restoreFakeAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	raw, _ := json.Marshal(params)
	switch method {
	case "file.mkdir":
		var p struct {
			Path string `json:"path"`
			Mode int    `json:"mode"`
		}
		_ = json.Unmarshal(raw, &p)
		if err := os.MkdirAll(p.Path, 0o755); err != nil {
			return nil, err
		}
	case "file.write":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(raw, &p)
		if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p.Path, []byte(p.Content), 0o600); err != nil {
			return nil, err
		}
	}
	return []byte(`{"status":"ok"}`), nil
}

// writeTestArchive builds a gzipped tar with the member naming Export
// produces: each source directory stored under its own top-level name.
func writeTestArchive(t *testing.T, path string, members map[string]string) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range members {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o600,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
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

// TestImportRestoresEachDirectoryToItsOwnRoot is the regression test for
// a restore that put everything under the config directory. Export names
// members by top-level directory while Import joined them all onto
// configDir, so router.yaml landed at <configDir>/lankeeper/router.yaml
// and the DNS daemons never saw their configuration.
//
// Mutates the process-global agent client, so no t.Parallel here.
func TestImportRestoresEachDirectoryToItsOwnRoot(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")
	unbound := filepath.Join(root, "unbound")
	openvpn := filepath.Join(root, "openvpn")

	origExtra := backupExtraDirs
	backupExtraDirs = []string{unbound, openvpn}
	t.Cleanup(func() { backupExtraDirs = origExtra })

	netutil.SetAgentClient(&restoreFakeAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	archive := filepath.Join(root, "backup.tar.gz")
	writeTestArchive(t, archive, map[string]string{
		"lankeeper/router.yaml": "lan: 10.10.10.0/24\n",
		"unbound/unbound.conf":  "server:\n",
		"openvpn/pki/ca.crt":    "CA CERT\n",
	})

	svc := NewBackupService(cfgDir)
	if err := svc.Import(context.Background(), archive, ""); err != nil {
		t.Fatalf("import: %v", err)
	}

	want := map[string]string{
		filepath.Join(cfgDir, "router.yaml"):    "lan: 10.10.10.0/24\n",
		filepath.Join(unbound, "unbound.conf"):  "server:\n",
		filepath.Join(openvpn, "pki", "ca.crt"): "CA CERT\n",
	}
	for path, content := range want {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("expected %s to be restored: %v", path, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s = %q, want %q", path, got, content)
		}
	}

	// The old behaviour nested everything one level deeper.
	if _, err := os.Stat(filepath.Join(cfgDir, "lankeeper")); err == nil {
		t.Error("member was restored under a duplicated directory level")
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "unbound")); err == nil {
		t.Error("unbound config was restored inside the config directory")
	}
}

// TestImportSkipsUnknownTopLevelDirectory keeps an archive written by a
// newer release restorable for the parts this binary understands.
func TestImportSkipsUnknownTopLevelDirectory(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")

	origExtra := backupExtraDirs
	backupExtraDirs = nil
	t.Cleanup(func() { backupExtraDirs = origExtra })

	netutil.SetAgentClient(&restoreFakeAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	archive := filepath.Join(root, "backup.tar.gz")
	writeTestArchive(t, archive, map[string]string{
		"lankeeper/router.yaml":   "cfg\n",
		"somefuturething/data.db": "future\n",
	})

	svc := NewBackupService(cfgDir)
	if err := svc.Import(context.Background(), archive, ""); err != nil {
		t.Fatalf("import should tolerate an unknown entry: %v", err)
	}

	if _, err := os.ReadFile(filepath.Join(cfgDir, "router.yaml")); err != nil {
		t.Errorf("known member was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "somefuturething")); err == nil {
		t.Error("unknown member was written to disk")
	}
}

// TestImportRejectsTraversal confirms the existing guard still holds
// after the destination became per-member.
func TestImportRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")

	netutil.SetAgentClient(&restoreFakeAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	archive := filepath.Join(root, "backup.tar.gz")
	writeTestArchive(t, archive, map[string]string{
		"lankeeper/../../escape.txt": "pwned\n",
	})

	svc := NewBackupService(cfgDir)
	err := svc.Import(context.Background(), archive, "")
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if !strings.Contains(err.Error(), "unsafe tar member") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestResolveRestoreTarget covers the mapping in isolation.
func TestResolveRestoreTarget(t *testing.T) {
	roots := restoreRoots("/etc/lankeeper", []string{"/etc/unbound", "/etc/openvpn"})

	cases := []struct {
		member string
		want   string
		wantOK bool
	}{
		{"lankeeper/router.yaml", "/etc/lankeeper/router.yaml", true},
		{"lankeeper", "/etc/lankeeper", true},
		{"unbound/unbound.conf", "/etc/unbound/unbound.conf", true},
		{"openvpn/pki/ca.crt", "/etc/openvpn/pki/ca.crt", true},
		{"dnsmasq.d/lan.conf", "", false},
		{"unknown/x", "", false},
	}

	for _, tc := range cases {
		got, ok := resolveRestoreTarget(roots, tc.member)
		if ok != tc.wantOK {
			t.Errorf("resolveRestoreTarget(%q) ok = %v, want %v", tc.member, ok, tc.wantOK)
			continue
		}
		if got != tc.want {
			t.Errorf("resolveRestoreTarget(%q) = %q, want %q", tc.member, got, tc.want)
		}
	}
}
