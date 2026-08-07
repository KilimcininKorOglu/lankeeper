package web_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/web"
)

// csrfPost drives one POST through the middleware with the given cookie
// and submitted token, and reports the status.
func csrfPost(t *testing.T, cookieValue, submitted string, viaHeader bool) int {
	t.Helper()

	wrapped := web.CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var req *http.Request
	if viaHeader {
		req = httptest.NewRequest(http.MethodPost, "/settings", nil)
		req.Header.Set("X-CSRF-Token", submitted)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/settings",
			strings.NewReader("csrf_token="+submitted))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookieValue != "" {
		req.AddCookie(&http.Cookie{Name: "csrf_token", Value: cookieValue})
	}

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	return rec.Code
}

// TestAMatchingTokenIsAccepted keeps the constant-time comparison from
// becoming a regression: the whole middleware is useless if it rejects
// the token it issued.
func TestAMatchingTokenIsAccepted(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	if code := csrfPost(t, token, token, true); code != http.StatusOK {
		t.Errorf("a matching header token was rejected: %d", code)
	}
	if code := csrfPost(t, token, token, false); code != http.StatusOK {
		t.Errorf("a matching form token was rejected: %d", code)
	}
}

// TestAMismatchedTokenIsRejected covers the ordinary failure, including
// the near-miss that a byte-at-a-time comparison would leak the shape of.
func TestAMismatchedTokenIsRejected(t *testing.T) {
	const cookie = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	cases := map[string]string{
		"completely different": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"differs in the last":  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdee",
		"differs in the first": "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"a prefix":             "0123456789abcdef",
		"cookie plus a byte":   cookie + "0",
	}
	for name, submitted := range cases {
		t.Run(name, func(t *testing.T) {
			if code := csrfPost(t, cookie, submitted, true); code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", code)
			}
		})
	}
}

// TestAnEmptySubmissionIsRejected is why the empty check stays separate
// from the comparison. subtle.ConstantTimeCompare returns 1 for two
// empty slices, so a request submitting nothing would match a cookie
// that failed to be set and pass the gate entirely.
func TestAnEmptySubmissionIsRejected(t *testing.T) {
	if code := csrfPost(t, "", "", true); code != http.StatusForbidden {
		t.Errorf("a request with neither cookie nor token got %d, want 403", code)
	}

	// The cookie exists but is empty, which is the case the comparison
	// alone would wave through.
	wrapped := web.CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/settings", nil)
	req.Header.Set("X-CSRF-Token", "")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: ""})
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("an empty token matched an empty cookie: %d", rec.Code)
	}
}

// TestAMissingCookieIsRejectedBeforeAnyComparison keeps the earlier gate
// intact, so a submitted token cannot stand in for one that was issued.
func TestAMissingCookieIsRejectedBeforeAnyComparison(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	if code := csrfPost(t, "", token, true); code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 when no cookie was issued", code)
	}
}

// TestTheTokenIsComparedInConstantTime is the regression test, and it
// reads the source because the property is not observable from
// behaviour: a plain != and a constant-time compare accept and reject
// exactly the same inputs. The difference is only in how long the
// rejection takes, and timing a Go string comparison from a test would
// be a flaky measurement of the machine rather than of the code.
//
// A plain != short-circuits at the first differing byte, which in
// principle leaks the token one byte at a time.
func TestTheTokenIsComparedInConstantTime(t *testing.T) {
	raw, err := os.ReadFile("middleware.go")
	if err != nil {
		t.Fatalf("read middleware.go: %v", err)
	}
	body := string(raw)

	start := strings.Index(body, "func CSRFProtect(")
	if start < 0 {
		t.Fatal("CSRFProtect is gone")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of CSRFProtect")
	}
	fn := body[start : start+end]

	if !strings.Contains(fn, "subtle.ConstantTimeCompare") {
		t.Error("the token comparison is not constant time")
	}
	if regexp.MustCompile(`token\s*!=\s*cookie\.Value`).MatchString(fn) {
		t.Error("the short-circuiting comparison is back")
	}
}
