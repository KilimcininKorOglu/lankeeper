package web

import (
	"log"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points the credential key at a temp file for the whole
// package. Saving a config that holds any key or preshared key needs it,
// and the default location under /var/lib is not writable in a test. A
// test that checks the key itself still sets its own path with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lankeeper-web-test-")
	if err != nil {
		log.Fatalf("create temp dir: %v", err)
	}
	if err := os.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(dir, "config.key")); err != nil {
		log.Fatalf("set key path: %v", err)
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("remove temp dir: %v", err)
	}
	os.Exit(code)
}
