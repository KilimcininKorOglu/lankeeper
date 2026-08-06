package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// TestImportRejectsAbsoluteMember covers the second half of the
// traversal guard. A member named with a leading separator would
// otherwise be written wherever it points, and Import writes through
// the root agent, so the primitive being guarded is an arbitrary
// root-owned file write.
func TestImportRejectsAbsoluteMember(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")

	netutil.SetAgentClient(&restoreFakeAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	archive := filepath.Join(root, "backup.tar.gz")
	writeTestArchive(t, archive, map[string]string{
		"/etc/cron.d/pwned": "* * * * * root sh -c id\n",
	})

	svc := NewBackupService(cfgDir)
	err := svc.Import(context.Background(), archive, "")
	if err == nil {
		t.Fatal("an absolute member path was accepted")
	}
	if !strings.Contains(err.Error(), "unsafe tar member") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestResolveRestoreTargetRefusesToEscapeItsOwnRoot pins the
// containment check that runs after the top-level lookup.
//
// The `..` rejection upstream means Import cannot reach this branch
// today, so it is defence in depth rather than the active guard. It is
// tested directly because that is the only way to reach it, and because
// a future change to the upstream check would make it load-bearing
// without anything else noticing.
func TestResolveRestoreTargetRefusesToEscapeItsOwnRoot(t *testing.T) {
	roots := restoreRoots("/etc/lankeeper", []string{"/etc/unbound"})

	// A member whose top level resolves but whose remainder climbs out.
	if got, ok := resolveRestoreTarget(roots, "lankeeper/../../etc/shadow"); ok {
		t.Errorf("a member escaping its own root resolved to %q", got)
	}
}

// TestImportCapsAMemberAtTheSizeLimit covers the per-entry read bound.
// Without it a crafted archive could make the unprivileged process
// buffer an arbitrary amount before handing it to the root agent.
func TestImportCapsAMemberAtTheSizeLimit(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	netutil.SetAgentClient(&restoreFakeAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	// One member larger than the 10 MiB per-entry limit.
	const oversize = 11 << 20
	archive := filepath.Join(root, "backup.tar.gz")
	writeSizedArchive(t, archive, "lankeeper/big.bin", oversize)

	svc := NewBackupService(cfgDir)
	if err := svc.Import(context.Background(), archive, ""); err != nil {
		t.Fatalf("import: %v", err)
	}

	written, err := os.Stat(filepath.Join(cfgDir, "big.bin"))
	if err != nil {
		t.Fatalf("member not written: %v", err)
	}
	if written.Size() >= oversize {
		t.Errorf("member written at %d bytes, the per-entry limit did not apply", written.Size())
	}
}

// writeSizedArchive builds a gzipped tar with a single member of the
// requested size, so the per-entry limit can be exercised without
// holding a large buffer in the assertion.
func writeSizedArchive(t *testing.T, path, name string, size int) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	hdr := &tar.Header{
		Name:     name,
		Mode:     0o600,
		Size:     int64(size),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	chunk := bytes.Repeat([]byte("A"), 64<<10)
	for written := 0; written < size; {
		n := len(chunk)
		if remaining := size - written; remaining < n {
			n = remaining
		}
		if _, err := tw.Write(chunk[:n]); err != nil {
			t.Fatalf("tar body: %v", err)
		}
		written += n
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

// TestEncryptedExportImportRoundTrip is the end-to-end path an operator
// actually uses: take an encrypted backup, then restore it. It covers
// Export, the encrypt and decrypt halves, and Import together, which no
// other test does; the export tests exercise only the argument builder.
//
// agentClient is left nil so netutil.Run shells out to the real tar,
// which is what makes this a genuine round trip rather than a
// reimplementation of the archive format.
func TestEncryptedExportImportRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}

	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const secret = "sessionSecret: keep-me\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "router.yaml"), []byte(secret), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// Only the config directory participates; the extra dirs point at
	// system paths this test must not touch.
	origExtra := backupExtraDirs
	backupExtraDirs = nil
	t.Cleanup(func() { backupExtraDirs = origExtra })

	// Local mode: no agent, so Run and WriteFile hit the real system.
	netutil.SetAgentClient(nil)

	svc := NewBackupService(cfgDir)
	archive := filepath.Join(root, "backup.tar.gz.enc")

	const passphrase = "correct horse battery staple"
	if err := svc.Export(context.Background(), archive, passphrase); err != nil {
		t.Fatalf("export: %v", err)
	}

	// An encrypted archive must not be readable as a plain gzip stream.
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if bytes.Contains(raw, []byte("keep-me")) {
		t.Error("the passphrase-protected archive contains the config in cleartext")
	}
	if _, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
		t.Error("the encrypted archive still parses as gzip, so it was not encrypted")
	}

	// Wipe the config and restore it from the archive.
	if err := os.Remove(filepath.Join(cfgDir, "router.yaml")); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	if err := svc.Import(context.Background(), archive, passphrase); err != nil {
		t.Fatalf("import: %v", err)
	}

	restored, err := os.ReadFile(filepath.Join(cfgDir, "router.yaml"))
	if err != nil {
		t.Fatalf("config not restored: %v", err)
	}
	if string(restored) != secret {
		t.Errorf("restored config = %q, want %q", restored, secret)
	}
}

// TestImportRejectsTheWrongPassphrase keeps a failed decrypt from being
// mistaken for a corrupt archive, and from overwriting live config with
// garbage.
func TestImportRejectsTheWrongPassphrase(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}

	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "router.yaml"), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	origExtra := backupExtraDirs
	backupExtraDirs = nil
	t.Cleanup(func() { backupExtraDirs = origExtra })

	netutil.SetAgentClient(nil)

	svc := NewBackupService(cfgDir)
	archive := filepath.Join(root, "backup.tar.gz.enc")
	if err := svc.Export(context.Background(), archive, "right-passphrase"); err != nil {
		t.Fatalf("export: %v", err)
	}

	err := svc.Import(context.Background(), archive, "wrong-passphrase")
	if err == nil {
		t.Fatal("import accepted the wrong passphrase")
	}
	if !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("error does not identify the decrypt failure: %v", err)
	}
}
