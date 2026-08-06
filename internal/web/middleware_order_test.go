package web_test

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/web"
	webfs "github.com/KilimcininKorOglu/lankeeper/web"
)

// newTestServer builds the real server so the assertion runs against
// the shipped middleware chain rather than a hand-rebuilt copy.
func newTestServer(t *testing.T) *web.Server {
	t.Helper()

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	// Keep the firewall watchdog state out of /var/lib during tests.
	t.Setenv("LANKEEPER_FIREWALL_STATE", filepath.Join(t.TempDir(), "firewall-pending.json"))

	loc, err := i18n.New("en")
	if err != nil {
		t.Fatalf("init i18n: %v", err)
	}
	if err := loc.LoadFromFS(webfs.EmbeddedFS, "locales"); err != nil {
		t.Fatalf("load locales: %v", err)
	}

	srv, err := web.NewServer(cfg, loc, webfs.EmbeddedFS,
		services.NewUpdateService("v0.0.0-test", "", "", nil))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

// captureLog redirects the standard logger for the duration of the
// test. log output is process-global, so no t.Parallel here.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

// TestSecurityRejectionsAreLogged is the regression test. RequestLogger
// used to be the innermost wrapper, so it ran only after every gate had
// passed. LANOnly, the rate limiter and CSRFProtect all short-circuit
// without calling next, so their rejections produced no log line at
// all: a lockout or an off-subnet probe left nothing to read.
func TestSecurityRejectionsAreLogged(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	t.Run("off-LAN source is logged", func(t *testing.T) {
		buf := captureLog(t)

		req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		req.RemoteAddr = "203.0.113.9:41234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected the LAN-only gate to reject, got %d", rec.Code)
		}
		if !strings.Contains(buf.String(), "203.0.113.9") {
			t.Errorf("rejected off-LAN request produced no log line: %q", buf.String())
		}
	})

	t.Run("rejected CSRF token is logged", func(t *testing.T) {
		buf := captureLog(t)

		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(""))
		req.RemoteAddr = "10.10.10.55:41234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected the CSRF gate to reject, got %d", rec.Code)
		}
		if !strings.Contains(buf.String(), "/login") {
			t.Errorf("rejected CSRF request produced no log line: %q", buf.String())
		}
	})
}
