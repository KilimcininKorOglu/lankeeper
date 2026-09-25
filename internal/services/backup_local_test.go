package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// Tests use a relaxed LocalBackupRoot pointer indirection so we can
// retarget the production constant at a TempDir for the duration of
// the test. Resetting in t.Cleanup keeps cross-test state pristine.

func withLocalRoot(t *testing.T, dir string) {
	t.Helper()
	orig := backupRootForTesting
	backupRootForTesting = dir + "/"
	t.Cleanup(func() { backupRootForTesting = orig })
}

func TestValidateLocalPath(t *testing.T) {
	tmp := t.TempDir()
	withLocalRoot(t, tmp)

	cases := []struct {
		spec    string
		wantErr bool
	}{
		{"", false},                // default → root
		{tmp, false},               // exact root
		{tmp + "/sub", false},      // child
		{"/etc/passwd", true},      // outside root
		{"relative/path", true},    // not absolute
		{tmp + "/../escape", true}, // traversal cleaned
	}
	for _, tc := range cases {
		_, err := validateLocalPath(tc.spec)
		if tc.wantErr && err == nil {
			t.Errorf("%q: expected error", tc.spec)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%q: %v", tc.spec, err)
		}
	}
}

func TestUploadLocalCopiesAndAtomicRename(t *testing.T) {
	tmp := t.TempDir()
	withLocalRoot(t, tmp)

	src := filepath.Join(tmp, "src.tar.gz.enc")
	if err := os.WriteFile(src, []byte("test-payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := config.BackupTarget{Type: "local", Path: tmp}
	got, err := uploadLocal(src, target)
	if err != nil {
		t.Fatalf("uploadLocal: %v", err)
	}
	want := filepath.Join(tmp, "src.tar.gz.enc")
	if got != want {
		t.Errorf("path = %s, want %s", got, want)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test-payload" {
		t.Errorf("payload = %q", data)
	}
	// No leftover .tmp.
	if _, err := os.Stat(want + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp leftover: %v", err)
	}
}

func TestCleanupLocalRetention(t *testing.T) {
	tmp := t.TempDir()
	withLocalRoot(t, tmp)

	writeStairStepBackups(t, tmp, 5)
	// Decoy that must NOT be touched.
	decoy := filepath.Join(tmp, "manual-rsync.tar")
	if err := os.WriteFile(decoy, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := config.BackupTarget{Type: "local", Path: tmp}
	deleted, err := cleanupLocal(target, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 3 {
		t.Errorf("deleted count = %d, want 3", len(deleted))
	}
	for _, d := range deleted {
		if !strings.HasPrefix(filepath.Base(d), "lankeeper-backup-") {
			t.Errorf("deleted non-backup file: %s", d)
		}
	}
	if _, err := os.Stat(decoy); err != nil {
		t.Errorf("decoy was deleted: %v", err)
	}

	// Two newest survive (indices 3 and 4 → ...04 and ...05).
	if count := countBackupFiles(t, tmp); count != 2 {
		t.Errorf("survivors = %d, want 2", count)
	}
}

// writeStairStepBackups writes n backup files whose mtimes rise a minute
// apart, oldest first.
func writeStairStepBackups(t *testing.T, dir string, n int) {
	t.Helper()
	now := time.Now()
	for i := range n {
		p := filepath.Join(dir, "lankeeper-backup-2026-05-0"+string(rune('1'+i))+".tar.gz.enc")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mtime := now.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// countBackupFiles counts the lankeeper backup files in dir.
func countBackupFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "lankeeper-backup-") {
			count++
		}
	}
	return count
}

func TestCleanupLocalRejectsZeroRetention(t *testing.T) {
	if _, err := cleanupLocal(config.BackupTarget{Type: "local"}, 0); err == nil {
		t.Error("expected error for retention=0")
	}
}
