package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// writeArchive builds a gzipped tar from a callback, so each test can
// shape the member stream it needs without a fixture file.
func writeArchive(t *testing.T, path string, build func(tw *tar.Writer)) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	build(tw)
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

func addFile(t *testing.T, tw *tar.Writer, name string, size int) {
	t.Helper()

	body := bytes.Repeat([]byte("a"), size)
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header %s: %v", name, err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar body %s: %v", name, err)
	}
}

// importTestService points a service at a temp config directory with the
// extra system directories disabled, so nothing escapes the sandbox.
//
// Mutates the process-global agent client, so no t.Parallel here.
func importTestService(t *testing.T) (*BackupService, string) {
	t.Helper()

	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")

	origExtra := backupExtraDirs
	backupExtraDirs = nil
	t.Cleanup(func() { backupExtraDirs = origExtra })

	netutil.SetAgentClient(&restoreFakeAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	return NewBackupService(cfgDir), root
}

// TestAnArchiveWithTooManyEntriesIsRefused is the regression test. The
// per-entry LimitReader was the only size control, and the loop carried
// no counter, so an archive of very many small and highly compressible
// entries was unbounded in total. Every member is written through the
// root agent, so that is an unbounded volume of privileged writes under
// the config directory from a single upload.
func TestAnArchiveWithTooManyEntriesIsRefused(t *testing.T) {
	svc, root := importTestService(t)

	archive := filepath.Join(root, "backup.tar.gz")
	writeArchive(t, archive, func(tw *tar.Writer) {
		for i := 0; i <= maxImportEntries; i++ {
			addFile(t, tw, fmt.Sprintf("lankeeper/f%06d", i), 1)
		}
	})

	err := svc.Import(context.Background(), archive, "")
	if err == nil {
		t.Fatal("an archive past the entry cap was accepted")
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPaddingWithUnknownEntriesIsAlsoCounted covers the skip path. An
// entry whose top-level directory this binary does not restore is
// logged and skipped, so counting only restored members would leave the
// cheapest padding uncounted.
func TestPaddingWithUnknownEntriesIsAlsoCounted(t *testing.T) {
	svc, root := importTestService(t)

	archive := filepath.Join(root, "backup.tar.gz")
	writeArchive(t, archive, func(tw *tar.Writer) {
		for i := 0; i <= maxImportEntries; i++ {
			addFile(t, tw, fmt.Sprintf("somefuturething/f%06d", i), 1)
		}
	})

	if err := svc.Import(context.Background(), archive, ""); err == nil {
		t.Fatal("an archive padded with skipped entries was accepted")
	}
}

// TestAnOversizedMemberIsRefusedRatherThanTruncated covers the silent
// half. io.ReadAll(io.LimitReader(tr, cap)) returns exactly cap bytes
// with a nil error, so a member past the cap was written short with
// nothing reported: a restored blocklist came back cut off and looked
// restored.
func TestAnOversizedMemberIsRefusedRatherThanTruncated(t *testing.T) {
	svc, root := importTestService(t)

	archive := filepath.Join(root, "backup.tar.gz")
	writeArchive(t, archive, func(tw *tar.Writer) {
		addFile(t, tw, "lankeeper/blocklist.conf", int(maxImportEntryBytes)+1)
	})

	err := svc.Import(context.Background(), archive, "")
	if err == nil {
		t.Fatal("an oversized member was accepted")
	}
	if !strings.Contains(err.Error(), "entry limit") {
		t.Errorf("unexpected error: %v", err)
	}

	// The truncated file must not be left behind looking like a restore.
	if _, statErr := os.Stat(filepath.Join(root, "lankeeper", "blocklist.conf")); statErr == nil {
		t.Error("a truncated member was written to disk")
	}
}

// TestTheCumulativeBudgetIsEnforced covers the accumulator. It drives
// the budget directly rather than through Import: proving the same thing
// end to end would mean compressing and extracting a quarter of a
// gigabyte, which is a minute of test time for arithmetic.
func TestTheCumulativeBudgetIsEnforced(t *testing.T) {
	b := &importBudget{maxEntries: 100, maxEntry: 10, maxTotal: 25}

	for i := 0; i < 2; i++ {
		if err := b.countBytes("f", 10); err != nil {
			t.Fatalf("member %d was refused inside the budget: %v", i, err)
		}
	}
	if err := b.countBytes("f", 10); err == nil {
		t.Error("the budget was exceeded without an error")
	} else if !strings.Contains(err.Error(), "extracts more than") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestThePerEntryCapIsEnforcedByTheBudget pins the other half, and that
// an oversized member does not silently consume the total instead.
func TestThePerEntryCapIsEnforcedByTheBudget(t *testing.T) {
	b := &importBudget{maxEntries: 100, maxEntry: 10, maxTotal: 1000}

	if err := b.countBytes("big", 11); err == nil {
		t.Fatal("a member past the per-entry cap was accepted")
	}
	if b.total != 0 {
		t.Errorf("a refused member consumed %d bytes of the budget", b.total)
	}
}

// TestTheEntryCountIsEnforcedByTheBudget covers the counter on its own,
// so the end-to-end test does not have to be the only proof.
func TestTheEntryCountIsEnforcedByTheBudget(t *testing.T) {
	b := &importBudget{maxEntries: 2, maxEntry: 10, maxTotal: 1000}

	for i := 0; i < 2; i++ {
		if err := b.countEntry(); err != nil {
			t.Fatalf("entry %d was refused inside the cap: %v", i, err)
		}
	}
	if err := b.countEntry(); err == nil {
		t.Error("the entry cap was exceeded without an error")
	}
}

// TestTheShippedBudgetUsesTheConfiguredLimits ties the unit tests above
// to what Import actually runs with.
func TestTheShippedBudgetUsesTheConfiguredLimits(t *testing.T) {
	b := newImportBudget()
	if b.maxEntries != maxImportEntries || b.maxEntry != maxImportEntryBytes || b.maxTotal != maxImportTotalBytes {
		t.Errorf("newImportBudget = %+v, want the module constants", b)
	}
}

// TestARealisticArchiveStillRestores keeps the bounds from becoming a
// regression of their own. A deployment with hundreds of issued client
// certificates has to stay well inside them.
func TestARealisticArchiveStillRestores(t *testing.T) {
	svc, root := importTestService(t)

	archive := filepath.Join(root, "backup.tar.gz")
	writeArchive(t, archive, func(tw *tar.Writer) {
		addFile(t, tw, "lankeeper/router.yaml", 64<<10)
		for i := 0; i < 500; i++ {
			addFile(t, tw, fmt.Sprintf("lankeeper/pki/issued/client%03d.crt", i), 2<<10)
			addFile(t, tw, fmt.Sprintf("lankeeper/pki/private/client%03d.key", i), 2<<10)
		}
		// A DNS blocklist, the largest single file a real backup holds.
		addFile(t, tw, "lankeeper/blocklist.conf", 8<<20)
	})

	if err := svc.Import(context.Background(), archive, ""); err != nil {
		t.Fatalf("a realistic archive was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "lankeeper", "router.yaml")); err != nil {
		t.Errorf("the config was not restored: %v", err)
	}
}
