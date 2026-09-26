package services

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// The WireGuard keys and every other secret in router.yaml are
// encrypted with the credential key. A restore onto new hardware has no
// key, so an encrypted export has to carry it or the restored secrets
// cannot be read.
func TestEncryptedExportRestoresTheCredentialKey(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	useBackupStaging(t)
	root := t.TempDir()
	keyPath := filepath.Join(root, "credentials", "config.key")
	t.Setenv("LANKEEPER_CONFIG_KEY", keyPath)

	key := seedCredentialKey(t, keyPath)
	cfgDir := seedConfigOnly(t, root)
	svc := NewBackupService(cfgDir)
	archive := filepath.Join(root, "backup.tar.gz.enc")
	if err := svc.Export(context.Background(), archive, "pass"); err != nil {
		t.Fatalf("export: %v", err)
	}
	assertArchiveIsEncrypted(t, archive, hex.EncodeToString(key))

	// New hardware: no key at all.
	if err := os.RemoveAll(filepath.Dir(keyPath)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Import(context.Background(), archive, "pass"); err != nil {
		t.Fatalf("import: %v", err)
	}

	assertRestoredKey(t, keyPath, key)
}

// A plain archive never carries the key, so one that does was crafted,
// and installing it would let the uploader choose the key.
func TestPlainImportRefusesACredentialKey(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(root, "config.key"))
	netutil.SetAgentClient(&restoreFakeAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	archive := filepath.Join(root, "backup.tar.gz")
	writeTestArchive(t, archive, map[string]string{
		archiveKeyMember: strings.Repeat("ab", 32),
	})

	svc := NewBackupService(filepath.Join(root, "lankeeper"))
	if err := svc.Import(context.Background(), archive, ""); err == nil {
		t.Fatal("a plain archive installed a credential key")
	}
	if _, err := os.Stat(filepath.Join(root, "config.key")); err == nil {
		t.Fatal("key file written from a plain archive")
	}
}

func seedCredentialKey(t *testing.T, keyPath string) []byte {
	t.Helper()
	key, err := config.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveKey(keyPath, key); err != nil {
		t.Fatal(err)
	}
	return key
}

// seedConfigOnly creates a config directory and limits the export to it,
// in local mode, so the test never touches system paths.
func seedConfigOnly(t *testing.T, root string) string {
	t.Helper()
	cfgDir := filepath.Join(root, "lankeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "router.yaml"), []byte(validRouterYAML(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	origExtra := backupExtraDirs
	backupExtraDirs = nil
	t.Cleanup(func() { backupExtraDirs = origExtra })
	netutil.SetAgentClient(nil)
	return cfgDir
}

func assertRestoredKey(t *testing.T, keyPath string, want []byte) {
	t.Helper()
	restored, err := config.LoadKey(keyPath)
	if err != nil {
		t.Fatalf("credential key not restored: %v", err)
	}
	if hex.EncodeToString(restored) != hex.EncodeToString(want) {
		t.Fatal("restored credential key differs from the exported one")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("restored key mode = %v, want 0600", info.Mode().Perm())
	}
}
