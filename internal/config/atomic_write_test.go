package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestSaveNeedsAWritableDirectory pins the permission the installer has
// to grant.
//
// SaveToFile writes atomically: create a temp file beside the target,
// then rename over it. Both halves act on the directory, not the file,
// so a directory the process cannot write makes every runtime config
// change fail even when the YAML itself is group-readable.
//
// The installers shipped /etc/lankeeper as mode 750 owned root:service,
// which is exactly this case for the unprivileged serve process: the
// session secret generated at first boot could not be persisted, so
// every restart minted a new one and dropped all sessions, and the
// password-change handler answered 500.
//
// Skipped when running as root, which bypasses the permission bits.
func TestSaveNeedsAWritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores directory permissions")
	}

	dir := filepath.Join(t.TempDir(), "lankeeper")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "router.yaml")

	cfg := &config.Config{}
	cfg.SetFilePath(path)
	cfg.System.SessionSecret = "generated-at-first-boot"

	// Writable: the ordinary case.
	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save into a writable directory failed: %v", err)
	}

	// Read-and-traverse only, which is what mode 750 looks like to a
	// member of the owning group.
	if err := os.Chmod(dir, 0o550); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := cfg.SaveToFile(); err == nil {
		t.Error("save succeeded in a directory the process cannot write; " +
			"if this ever passes, re-check why the installer needs group write")
	}
}
