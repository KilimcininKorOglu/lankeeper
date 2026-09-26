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

	srv := newServerWithConfig(t, cfg)
	post := newFormClient(t, srv.Handler()).post

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
		"currentPassword": {"old-password"},
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

// newServerWithConfig builds the real server over the embedded assets with an
// English localizer.
func newServerWithConfig(t *testing.T, cfg *config.Config) *web.Server {
	t.Helper()
	// NewServer parses configs/sysconf/*.tmpl relative to the working
	// directory, as serve does from the data directory.
	t.Chdir("../..")
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

// formClient posts forms from a LAN address with the CSRF token and
// cookie a GET issued.
type formClient struct {
	t       *testing.T
	handler http.Handler
	csrf    string
	jar     []*http.Cookie
}

// newFormClient performs the GET that seeds the CSRF cookie and echoes
// the token, which every subsequent POST has to present.
func newFormClient(t *testing.T, handler http.Handler) *formClient {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.RemoteAddr = "10.10.10.20:5000"
	handler.ServeHTTP(rec, req)

	csrf := rec.Header().Get("X-CSRF-Token")
	if csrf == "" {
		t.Fatalf("no CSRF token issued (status %d)", rec.Code)
	}
	return &formClient{t: t, handler: handler, csrf: csrf, jar: rec.Result().Cookies()}
}

func (c *formClient) post(path string, form url.Values, extra []*http.Cookie) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", c.csrf)
	req.RemoteAddr = "10.10.10.20:5000"
	for _, ck := range append(c.jar, extra...) {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	return rec
}

// A session alone must not be enough to replace the admin password.
func TestPasswordChangeRequiresTheCurrentPassword(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.System.SessionSecret = "test-secret"
	cfg.System.AdminPasswordHash = hashOf(t, "old-password")
	t.Setenv("LANKEEPER_FIREWALL_STATE", filepath.Join(t.TempDir(), "firewall-pending.json"))

	srv := newServerWithConfig(t, cfg)
	post := newFormClient(t, srv.Handler()).post
	session := post("/login", url.Values{"password": {"old-password"}}, nil).Result().Cookies()

	for _, current := range []string{"", "wrong-password"} {
		rec := post("/settings/web-password", url.Values{
			"currentPassword": {current},
			"newPassword":     {"brand-new-password"},
			"confirmPassword": {"brand-new-password"},
		}, session)
		if rec.Code != http.StatusForbidden {
			t.Errorf("current=%q: status %d, want 403", current, rec.Code)
		}
	}
	if post("/login", url.Values{"password": {"old-password"}}, nil).Result().Cookies() == nil {
		t.Error("the old password stopped working after refused changes")
	}
}
