package services

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestBackupIncludesOpenVPNPKI covers the archive that omitted
// /etc/openvpn. The easy-rsa PKI lives only there: the CA key and
// certificate, the server certificate, every issued client certificate,
// and ta.key. None of it is mirrored into router.yaml, so an archive
// without it cannot restore a working OpenVPN server.
func TestBackupIncludesOpenVPNPKI(t *testing.T) {
	if !slices.Contains(backupExtraDirs, "/etc/openvpn") {
		t.Fatalf("backupExtraDirs = %v, must include /etc/openvpn", backupExtraDirs)
	}
}

// TestBuildExportArgsIncludesExistingDirs checks each present directory
// reaches tar as its own -C pair, so members are stored under a plain
// top-level name.
func TestBuildExportArgsIncludesExistingDirs(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")
	ovpn := filepath.Join(root, "openvpn")
	unbound := filepath.Join(root, "unbound")
	for _, d := range []string{cfgDir, ovpn, unbound} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	args := buildExportArgs("/tmp/out.tar.gz", cfgDir, []string{unbound, ovpn})

	if args[0] != "czf" || args[1] != "/tmp/out.tar.gz" {
		t.Fatalf("unexpected leading args: %v", args)
	}
	for _, want := range []string{"lankeeper", "unbound", "openvpn"} {
		if !slices.Contains(args, want) {
			t.Errorf("tar args missing %q: %v", want, args)
		}
	}
	// Each directory must be preceded by its own -C, otherwise tar
	// resolves it against the wrong working directory.
	for i, a := range args {
		if a == "openvpn" {
			if i < 2 || args[i-2] != "-C" || args[i-1] != root {
				t.Errorf("openvpn not preceded by -C %s: %v", root, args)
			}
		}
	}
}

// TestBuildExportArgsSkipsMissingDirs keeps a subsystem that was never
// configured from failing the whole archive.
func TestBuildExportArgsSkipsMissingDirs(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "lankeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	missing := filepath.Join(root, "openvpn")

	args := buildExportArgs("/tmp/out.tar.gz", cfgDir, []string{missing})

	if slices.Contains(args, "openvpn") {
		t.Errorf("absent directory reached tar: %v", args)
	}
	if !slices.Contains(args, "lankeeper") {
		t.Errorf("config directory dropped: %v", args)
	}
}
