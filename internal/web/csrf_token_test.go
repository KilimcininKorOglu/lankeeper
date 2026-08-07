package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAnExistingTokenIsReturnedUnchanged keeps the refactor from
// reissuing a cookie on every read, which would invalidate the token a
// page had already embedded in its form.
func TestAnExistingTokenIsReturnedUnchanged(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "already-issued"})
	rec := httptest.NewRecorder()

	token, err := getOrCreateCSRFToken(rec, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "already-issued" {
		t.Errorf("token = %q, want the one the caller already held", token)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a new cookie was issued over an existing token")
	}
}

// TestANewTokenIsMintedAndSet covers the other branch, including the
// cookie attributes the browser needs for the token to be usable.
func TestANewTokenIsMintedAndSet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	token, err := getOrCreateCSRFToken(rec, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 32 bytes of hex.
	if len(token) != 64 {
		t.Errorf("token is %d characters, want 64 hex digits", len(token))
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("%d cookies were set, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != "csrf_token" || c.Value != token {
		t.Errorf("cookie %s=%s does not carry the returned token", c.Name, c.Value)
	}
	// The form reads this from JavaScript, so HttpOnly stays off; the
	// rest has to hold.
	if !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie attributes weakened: secure=%v samesite=%v", c.Secure, c.SameSite)
	}
}

// TestTwoRequestsGetDistinctTokens keeps the value random rather than
// derived from something an attacker could reproduce.
func TestTwoRequestsGetDistinctTokens(t *testing.T) {
	mint := func() string {
		t.Helper()
		token, err := getOrCreateCSRFToken(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return token
	}

	if a, b := mint(), mint(); a == b {
		t.Errorf("two fresh tokens are identical: %s", a)
	}
}

// TestTheMiddlewareStillIssuesATokenOnGET covers the wiring, since the
// error return added a branch ahead of the header write.
func TestTheMiddlewareStillIssuesATokenOnGET(t *testing.T) {
	var reached bool
	wrapped := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("the wrapped handler was not reached")
	}
	if rec.Header().Get("X-CSRF-Token") == "" {
		t.Error("the response carries no X-CSRF-Token header")
	}
}
