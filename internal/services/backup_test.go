package services_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestFactoryResetWritesEmbeddedDefaults proves the reset reads the
// shipped defaults rather than a directory derived from configDir. The
// old implementation joined configDir's parent with "configs/defaults",
// which resolved to a path no install path ever creates.
func TestFactoryResetWritesEmbeddedDefaults(t *testing.T) {
	dst := t.TempDir()

	svc := services.NewBackupService(dst)
	if err := svc.FactoryReset(context.Background()); err != nil {
		t.Fatalf("FactoryReset: %v", err)
	}

	// router.yaml is the file the appliance cannot boot without.
	for _, name := range []string{"router.yaml", "firewall.yaml", "vpn.yaml"} {
		info, err := os.Stat(filepath.Join(dst, name))
		if err != nil {
			t.Errorf("expected %s to be restored: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s restored empty", name)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Errorf("%s mode = %o, want 640", name, got)
		}
	}
}

// TestFactoryResetReportsMissingDefaults guards the failure mode the
// original code had: returning nil after writing nothing, which let the
// handler reboot the router while reporting success.
func TestFactoryResetReportsMissingDefaults(t *testing.T) {
	empty := fstest.MapFS{"defaults/README.md": &fstest.MapFile{Data: []byte("not yaml")}}

	svc := services.NewBackupServiceWithDefaults(t.TempDir(), empty)
	err := svc.FactoryReset(context.Background())
	if err == nil {
		t.Fatal("expected an error when no default YAML exists, got nil")
	}
}

// TestFactoryResetRestoresFromInjectedFS confirms the injected source is
// honoured and its contents land verbatim in configDir.
func TestFactoryResetRestoresFromInjectedFS(t *testing.T) {
	want := []byte("lan:\n  subnet: 10.10.10.0/24\n")
	src := fstest.MapFS{
		"defaults/router.yaml": &fstest.MapFile{Data: want},
		"defaults/notes.txt":   &fstest.MapFile{Data: []byte("ignored")},
	}
	dst := t.TempDir()

	svc := services.NewBackupServiceWithDefaults(dst, src)
	if err := svc.FactoryReset(context.Background()); err != nil {
		t.Fatalf("FactoryReset: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "router.yaml"))
	if err != nil {
		t.Fatalf("read restored router.yaml: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("router.yaml = %q, want %q", got, want)
	}

	if _, err := os.Stat(filepath.Join(dst, "notes.txt")); !os.IsNotExist(err) {
		t.Error("non-YAML entry should not be restored")
	}
}

// TestEmbeddedDefaultsArePresent fails the build-time contract loudly if
// the embed directive ever stops matching the shipped YAML set.
func TestEmbeddedDefaultsArePresent(t *testing.T) {
	dst := t.TempDir()
	svc := services.NewBackupService(dst)
	if err := svc.FactoryReset(context.Background()); err != nil {
		t.Fatalf("FactoryReset: %v", err)
	}

	entries, err := fs.Glob(os.DirFS(dst), "*.yaml")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) < 6 {
		t.Errorf("restored %d YAML files (%v), want the full shipped set of 6", len(entries), entries)
	}
}
