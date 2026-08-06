package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/web"
	webfs "github.com/KilimcininKorOglu/lankeeper/web"
)

func hashOf(t *testing.T, password string) string {
	t.Helper()
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return string(b)
}

// TestSetPasswordHashSwapsTheAcceptedCredential covers the auth object
// on its own: the hash is cached by value, so without a way to replace
// it a password change could not affect what login accepts.
func TestSetPasswordHashSwapsTheAcceptedCredential(t *testing.T) {
	auth := web.NewAuth("test-secret", hashOf(t, "old-password"))

	if !auth.VerifyPassword("old-password") {
		t.Fatal("the original password was rejected")
	}

	auth.SetPasswordHash(hashOf(t, "new-password"))

	if auth.VerifyPassword("old-password") {
		t.Error("the old password is still accepted after the change")
	}
	if !auth.VerifyPassword("new-password") {
		t.Error("the new password is not accepted after the change")
	}
}

// TestPasswordChangeTakesEffectImmediately is the regression test, run
// against the real server so it covers the wiring and not just the
// setter. Before the fix the handler persisted the new hash and
// reported success while login kept accepting the old password until
// the process restarted, which defeats rotating a leaked credential.
func TestPasswordChangeTakesEffectImmediately(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.System.SessionSecret = "test-secret"
	cfg.System.AdminPasswordHash = hashOf(t, "old-password")
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
	handler := srv.Handler()

	// A GET seeds the CSRF cookie and echoes the token, which every
	// subsequent POST has to present.
	getRec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/login", nil)
	getReq.RemoteAddr = "10.10.10.20:5000"
	handler.ServeHTTP(getRec, getReq)

	csrf := getRec.Header().Get("X-CSRF-Token")
	if csrf == "" {
		t.Fatalf("no CSRF token issued (status %d)", getRec.Code)
	}
	jar := getRec.Result().Cookies()

	post := func(path string, form url.Values, extra []*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-CSRF-Token", csrf)
		req.RemoteAddr = "10.10.10.20:5000"
		for _, c := range jar {
			req.AddCookie(c)
		}
		for _, c := range extra {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Log in with the original password to obtain a session, which the
	// password-change route requires.
	loginRec := post("/login", url.Values{"password": {"old-password"}}, nil)
	session := loginRec.Result().Cookies()
	if len(session) == 0 {
		t.Fatalf("login did not set a session cookie (status %d, body %s)",
			loginRec.Code, loginRec.Body.String())
	}

	// Change the password through the route the operator uses.
	changeRec := post("/settings/web-password", url.Values{
		"newPassword":     {"brand-new-password"},
		"confirmPassword": {"brand-new-password"},
	}, session)

	if changeRec.Code >= 400 {
		t.Fatalf("password change returned %d: %s", changeRec.Code, changeRec.Body.String())
	}

	// The config must hold the new hash...
	if bcrypt.CompareHashAndPassword([]byte(cfg.System.AdminPasswordHash),
		[]byte("brand-new-password")) != nil {
		t.Fatal("config was not updated with the new password hash")
	}

	// ...and, the actual point, the live auth object must agree.
	if srv.Auth().VerifyPassword("old-password") {
		t.Error("the old password still works after a successful change")
	}
	if !srv.Auth().VerifyPassword("brand-new-password") {
		t.Error("the new password does not work until a restart")
	}
}
