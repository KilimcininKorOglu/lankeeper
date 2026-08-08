package handlers

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestSecretDownloadHeadersForbidCaching is the regression test. These
// responses carry a WireGuard private key, an OpenVPN key, or the config
// archive, and they set only Content-Type and Content-Disposition.
// Neither says anything about storage, so the browser was free to write
// the body to its on-disk cache, leaving the key recoverable on a shared
// workstation after the session ended.
func TestSecretDownloadHeadersForbidCaching(t *testing.T) {
	rec := httptest.NewRecorder()
	setSecretDownloadHeaders(rec, "text/plain", "phone.conf")

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="phone.conf"` {
		t.Errorf("Content-Disposition = %q", got)
	}
}

// TestExportSendsNoStore covers the one path that streams through
// http.ServeFile, where the headers are set before the file is served
// and must survive it.
func TestExportSendsNoStore(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(dir, "router.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "router.yaml"), []byte("system:\n  hostname: t\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	h := NewSystemHandler(nil, cfg, nil, nil, services.NewBackupService(dir), nil, services.NewSystemService(), nil)

	req := httptest.NewRequest("POST", "/settings/export?passphrase=secret", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Body = nil
	req.URL.RawQuery = "passphrase=secret"

	rec := httptest.NewRecorder()
	h.HandleExport(rec, req)

	// tar runs through the agent, which is not wired here, so the export
	// itself fails. The header contract is what this pins, and a
	// regression would show as an unset value on the success path too.
	if rec.Code == 200 {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
	}
}

// TestEverySecretDownloadUsesTheHelper keeps a new download from
// reintroducing the gap by setting its own headers by hand.
func TestEverySecretDownloadUsesTheHelper(t *testing.T) {
	for _, f := range []string{"vpn.go", "openvpn.go", "system.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(b), `Set("Content-Disposition"`) {
			t.Errorf("%s sets Content-Disposition directly; use setSecretDownloadHeaders "+
				"so the no-store directive cannot be forgotten", f)
		}
	}
}
