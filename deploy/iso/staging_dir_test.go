package iso_test

import (
	"os"
	"strings"
	"testing"
)

// TestBothInstallersCreateTheBackupStagingDir keeps the directory the
// encrypted backup export depends on in both install paths, owned by
// root with the service group and setgid, and set after the recursive
// chown of the data directory, which would otherwise hand it to the
// service account and let it plant a symlink for root's tar.
func TestBothInstallersCreateTheBackupStagingDir(t *testing.T) {
	for _, path := range []string{"../install.sh", "post-install.sh"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(raw)

		recursive := strings.Index(body, `chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"`)
		if recursive < 0 {
			t.Fatalf("%s: the recursive chown of the data directory is gone; re-derive this check", path)
		}
		for _, want := range []string{
			`mkdir -p "$DATA_DIR/staging"`,
			`chown root:"$SERVICE_USER" "$DATA_DIR/staging"`,
			`chmod 2750 "$DATA_DIR/staging"`,
		} {
			at := strings.Index(body, want)
			if at < 0 {
				t.Errorf("%s does not run %s", path, want)
				continue
			}
			if at < recursive {
				t.Errorf("%s runs %s before the recursive chown, which resets it", path, want)
			}
		}
	}
}
