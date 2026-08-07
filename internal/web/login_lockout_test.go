package web

import (
	"bytes"
	"log"
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
	webfs "github.com/KilimcininKorOglu/lankeeper/web"
)

// newLockoutTestServer builds a real server against a temporary config,
// so the login path is exercised through its actual wiring.
func newLockoutTestServer(t *testing.T, password string) *Server {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.System.SessionSecret = "test-secret"
	cfg.System.AdminPasswordHash = string(hash)
	t.Setenv("LANKEEPER_FIREWALL_STATE", filepath.Join(t.TempDir(), "firewall-pending.json"))

	loc, err := i18n.New("en")
	if err != nil {
		t.Fatalf("init i18n: %v", err)
	}
	if err := loc.LoadFromFS(webfs.EmbeddedFS, "locales"); err != nil {
		t.Fatalf("load locales: %v", err)
	}

	srv, err := NewServer(cfg, loc, webfs.EmbeddedFS,
		services.NewUpdateService("v0.0.0-test", "", "", nil))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	// Each server owns four sweeper goroutines. Leaving them running
	// seeds the next test's measurements with stragglers, and Stop is
	// idempotent, so the one test that stops them itself is unaffected.
	t.Cleanup(srv.stopBackgroundSweepers)
	return srv
}

// hasSessionCookie reports whether the response actually granted a
// session. The login page reissues a CSRF cookie on every render, so
// counting cookies would call a rejection a success.
func hasSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionName && c.MaxAge >= 0 {
			return true
		}
	}
	return false
}

func loginRequest(password, remoteAddr string) *http.Request {
	form := url.Values{"password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remoteAddr
	return req
}

// TestALockedOutAddressIsRejectedEvenWithTheRightPassword is the point
// of the ordering in the handler. If the password were checked first, a
// locked-out attacker could keep guessing and simply read the answer
// from the response they were given anyway.
//
// The handler is called directly rather than through the mux, because
// the rate limiter in front of the route would answer the later attempts
// itself and the guard's own behaviour would never be reached.
func TestALockedOutAddressIsRejectedEvenWithTheRightPassword(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const addr = "10.10.10.30:5000"
	const ip = "10.10.10.30"

	for i := 0; i < loginFailureThreshold; i++ {
		srv.loginGuard.RecordFailure(ip)
	}
	if srv.loginGuard.LockedFor(ip) == 0 {
		t.Fatal("the address was not locked out")
	}

	rec := httptest.NewRecorder()
	srv.handleLogin(rec, loginRequest("correct-horse", addr))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	// The login page reissues a CSRF cookie on every render, so only the
	// session cookie says anything about whether the attempt succeeded.
	if hasSessionCookie(rec) {
		t.Error("a session cookie was issued to a locked-out address")
	}
	if !strings.Contains(rec.Body.String(), "Too many failed attempts") {
		t.Errorf("the response does not explain the lockout: %s", rec.Body.String())
	}
	// The lockout knows exactly how long it has left, so the refusal
	// should say so rather than leaving a client to guess.
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("the lockout response carries no Retry-After")
	}
}

// TestTheFifthFailureAnswersWithTheLockout covers the response the
// operator actually sees at the threshold: a 429 naming the wait, not
// another indistinguishable 401.
func TestTheFifthFailureAnswersWithTheLockout(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const addr = "10.10.10.31:5000"

	for i := 1; i < loginFailureThreshold; i++ {
		rec := httptest.NewRecorder()
		srv.handleLogin(rec, loginRequest("wrong", addr))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	srv.handleLogin(rec, loginRequest("wrong", addr))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("attempt at the threshold: status = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Try again in") {
		t.Errorf("the response does not name a wait: %s", rec.Body.String())
	}
}

// TestASuccessfulLoginStillWorks keeps the guard from being a regression
// of its own.
func TestASuccessfulLoginStillWorks(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const addr = "10.10.10.32:5000"

	rec := httptest.NewRecorder()
	srv.handleLogin(rec, loginRequest("correct-horse", addr))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if !hasSessionCookie(rec) {
		t.Error("no session cookie was issued for a correct password")
	}
}

// TestAFailedLoginIsNamedInTheLog covers the detective half. The request
// logger records a status, so a rejection already leaves a 401 line, but
// that line is one of many and says nothing about how far a run has
// gone. An operator scanning for trouble needs the word and the count.
//
// log output is process-global, so no t.Parallel here.
func TestAFailedLoginIsNamedInTheLog(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	srv := newLockoutTestServer(t, "correct-horse")
	const addr = "10.10.10.33:5000"

	srv.handleLogin(httptest.NewRecorder(), loginRequest("wrong", addr))

	logged := buf.String()
	if !strings.Contains(logged, "auth: failed login from 10.10.10.33") {
		t.Errorf("the failure is not named in the log: %q", logged)
	}
	if !strings.Contains(logged, "(1 consecutive)") {
		t.Errorf("the log line does not carry the run length: %q", logged)
	}

	buf.Reset()
	for i := 1; i < loginFailureThreshold; i++ {
		srv.handleLogin(httptest.NewRecorder(), loginRequest("wrong", addr))
	}
	if !strings.Contains(buf.String(), "locked out for") {
		t.Errorf("the lockout is not recorded in the log: %q", buf.String())
	}
}

// TestASuccessAfterFailuresClearsTheRunEndToEnd checks the wiring of the
// reset, which the guard's own test can only prove in isolation.
func TestASuccessAfterFailuresClearsTheRunEndToEnd(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const addr = "10.10.10.34:5000"
	const ip = "10.10.10.34"

	for i := 1; i < loginFailureThreshold; i++ {
		srv.handleLogin(httptest.NewRecorder(), loginRequest("wrong", addr))
	}
	if got := srv.loginGuard.failureCount(ip); got != loginFailureThreshold-1 {
		t.Fatalf("failure count = %d, want %d", got, loginFailureThreshold-1)
	}

	rec := httptest.NewRecorder()
	srv.handleLogin(rec, loginRequest("correct-horse", addr))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the correct password was rejected below the threshold: %d", rec.Code)
	}
	if got := srv.loginGuard.failureCount(ip); got != 0 {
		t.Errorf("failure count after a success = %d, want 0", got)
	}
}
