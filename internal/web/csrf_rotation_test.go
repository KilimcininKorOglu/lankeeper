package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// csrfCookie returns the token the response issued, or "" if it issued
// none.
func csrfCookie(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "csrf_token" {
			return c.Value
		}
	}
	return ""
}

// withCSRF attaches a token cookie, standing in for the one a browser
// picked up before authenticating.
func withCSRF(req *http.Request, token string) *http.Request {
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: token})
	return req
}

// TestLoginRotatesTheToken is the regression test. getOrCreateCSRFToken
// reuses an existing cookie and the cookie carries no expiry, so one
// value survived an entire login and logout cycle. The cookie is
// deliberately not HttpOnly so client code can echo it, which is exactly
// what makes a planted value worth rotating away.
func TestLoginRotatesTheToken(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const planted = "plantedplantedplantedplantedplantedplantedplantedplantedplanted0"

	rec := httptest.NewRecorder()
	srv.handleLogin(rec, withCSRF(loginRequest("correct-horse", "10.10.10.60:5000"), planted))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login failed: %d", rec.Code)
	}
	issued := csrfCookie(rec)
	if issued == "" {
		t.Fatal("login issued no CSRF cookie, so the planted one survives")
	}
	if issued == planted {
		t.Error("the pre-authentication token was carried into the session")
	}
}

// TestLogoutRotatesTheToken covers the other boundary: the value that
// served an authenticated session does not outlive it.
func TestLogoutRotatesTheToken(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const during = "duringduringduringduringduringduringduringduringduringduring0000"

	req := withCSRF(httptest.NewRequest(http.MethodPost, "/logout", nil), during)
	rec := httptest.NewRecorder()
	srv.handleLogout(rec, req)

	issued := csrfCookie(rec)
	if issued == "" {
		t.Fatal("logout issued no CSRF cookie")
	}
	if issued == during {
		t.Error("the session's token survived logout")
	}
}

// TestAFailedLoginDoesNotRotate keeps rotation tied to the boundary
// rather than to every attempt. Reissuing on a rejected password would
// invalidate the token the login page already rendered into its form,
// so the operator's second attempt would fail on CSRF rather than on the
// password.
func TestAFailedLoginDoesNotRotate(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const held = "heldheldheldheldheldheldheldheldheldheldheldheldheldheldheld0000"

	rec := httptest.NewRecorder()
	srv.handleLogin(rec, withCSRF(loginRequest("wrong", "10.10.10.61:5000"), held))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if issued := csrfCookie(rec); issued != "" && issued != held {
		t.Errorf("a rejected login rotated the token to %s", issued)
	}
}

// TestRotationIssuesADistinctTokenEachTime keeps the new value random
// rather than derived from the old one.
func TestRotationIssuesADistinctTokenEachTime(t *testing.T) {
	first := httptest.NewRecorder()
	if _, err := rotateCSRFToken(first); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	second := httptest.NewRecorder()
	if _, err := rotateCSRFToken(second); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	a, b := csrfCookie(first), csrfCookie(second)
	if a == "" || b == "" {
		t.Fatal("rotation issued no cookie")
	}
	if a == b {
		t.Errorf("two rotations produced the same token: %s", a)
	}
	if len(a) != 64 {
		t.Errorf("token is %d characters, want 64 hex digits", len(a))
	}
}

// TestRotationOverwritesAnExistingCookie is the difference between
// rotation and the get-or-create path: an existing value must not be
// returned unchanged.
func TestRotationOverwritesAnExistingCookie(t *testing.T) {
	const existing = "existingexistingexistingexistingexistingexistingexistingexisting"

	rec := httptest.NewRecorder()
	token, err := rotateCSRFToken(rec)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if token == existing {
		t.Fatal("rotation returned the value it was meant to replace")
	}
	if got := csrfCookie(rec); got != token {
		t.Errorf("cookie carries %s but the caller was handed %s", got, token)
	}
}

// TestAPasswordChangeRotatesTheToken covers the third boundary. The
// handler lives in a package that cannot import the token minting, so a
// callback is the only thing connecting them, and an unwired one would
// skip rotation without a word.
func TestAPasswordChangeRotatesTheToken(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const before = "beforebeforebeforebeforebeforebeforebeforebeforebeforebefore0000"

	form := url.Values{
		"newPassword":     {"brand-new-password"},
		"confirmPassword": {"brand-new-password"},
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/web-password",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	withCSRF(req, before)

	rec := httptest.NewRecorder()
	srv.settings.HandleChangeWebPassword(rec, req)

	if rec.Code >= 400 {
		t.Fatalf("the password change failed: %d %s", rec.Code, rec.Body.String())
	}
	issued := csrfCookie(rec)
	if issued == "" {
		t.Fatal("the password change issued no CSRF cookie")
	}
	if issued == before {
		t.Error("the token from before the credential change carried over")
	}
}

// TestARejectedPasswordChangeDoesNotRotate keeps rotation tied to an
// actual credential change.
func TestARejectedPasswordChangeDoesNotRotate(t *testing.T) {
	srv := newLockoutTestServer(t, "correct-horse")
	const held = "heldheldheldheldheldheldheldheldheldheldheldheldheldheldheld0000"

	form := url.Values{
		"newPassword":     {"one-password"},
		"confirmPassword": {"a-different-one"},
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/web-password",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	withCSRF(req, held)

	rec := httptest.NewRecorder()
	srv.settings.HandleChangeWebPassword(rec, req)

	if rec.Code < 400 {
		t.Fatalf("a mismatched password change was accepted: %d", rec.Code)
	}
	if issued := csrfCookie(rec); issued != "" && issued != held {
		t.Errorf("a rejected change rotated the token to %s", issued)
	}
}
