package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// An htmx request with no session must make the browser navigate to the
// login page; a 303 is followed inside the XHR and the click does nothing.
func TestAuthRequiredSendsHtmxToTheLoginPage(t *testing.T) {
	h := AuthRequired(NewAuth("test-secret-test-secret-test-secret", "unused"))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the handler ran without a session")
	}))

	req := httptest.NewRequest(http.MethodPost, "/firewall/rules", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != "/login" {
		t.Errorf("htmx: status %d, HX-Redirect %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/firewall", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("navigation: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// An EventSource that follows the 303 receives the HTML login page and
// closes for good, so an SSE request with no session gets a plain 401.
func TestAuthRequiredRefusesAnSSEStreamWithoutRedirect(t *testing.T) {
	h := AuthRequired(NewAuth("test-secret-test-secret-test-secret", "unused"))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the handler ran without a session")
	}))

	req := httptest.NewRequest(http.MethodGet, "/events/stats", nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Location") != "" {
		t.Errorf("sse: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
}
