package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The CSRF check runs before authentication, so the body a client sends
// must be bounded there, not only in the handlers that expect uploads.
func TestCSRFProtectBoundsTheRequestBody(t *testing.T) {
	var readErr error
	h := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.Copy(io.Discard, r.Body)
	}))

	body := strings.NewReader(strings.Repeat("a", 4<<20))
	req := httptest.NewRequest(http.MethodPost, "/dns/records", body)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "tok"})
	req.Header.Set("X-CSRF-Token", "tok")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("a 4 MB body passed the CSRF middleware unbounded")
	}
}

func TestCSRFProtectRefusesAnOversizedFormWithoutHeader(t *testing.T) {
	called := false
	h := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	form := "pad=" + strings.Repeat("a", 2<<20) + "&csrf_token=tok"
	req := httptest.NewRequest(http.MethodPost, "/dns/records", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "tok"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called || rec.Code != http.StatusForbidden {
		t.Fatalf("oversized form: called=%v status=%d, want refusal with 403", called, rec.Code)
	}
}

func TestCSRFProtectStillAcceptsASmallForm(t *testing.T) {
	called := false
	h := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/dns/records", strings.NewReader("csrf_token=tok&name=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "tok"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("a valid small form was refused")
	}
}
