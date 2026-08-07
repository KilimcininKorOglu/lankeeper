package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRespondRefreshAnswersHtmxWithARefresh pins the contract the 57
// converted call sites had written out in full.
func TestRespondRefreshAnswersHtmxWithARefresh(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/backup/targets", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	respondRefresh(rec, req, "/backup")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("HX-Refresh"); got != "true" {
		t.Errorf("HX-Refresh = %q, want true", got)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Errorf("an htmx response carried a redirect to %q", got)
	}
}

// TestRespondRefreshRedirectsAPlainForm covers the other half: a full
// navigation needs somewhere to go, or the operator sees an empty body.
func TestRespondRefreshRedirectsAPlainForm(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/backup/targets", nil)
	rec := httptest.NewRecorder()

	respondRefresh(rec, req, "/backup")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/backup" {
		t.Errorf("Location = %q, want /backup", got)
	}
	if got := rec.Header().Get("HX-Refresh"); got != "" {
		t.Errorf("a plain form response carried HX-Refresh: %q", got)
	}
}

// TestRespondTriggerFiresTheNamedEvent covers the second contract, which
// the other 10 sites used.
func TestRespondTriggerFiresTheNamedEvent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/timezone", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	respondTrigger(rec, req, "settingsUpdated", "/settings")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "settingsUpdated" {
		t.Errorf("HX-Trigger = %q, want settingsUpdated", got)
	}
	if got := rec.Header().Get("HX-Refresh"); got != "" {
		t.Errorf("a trigger response also asked for a refresh: %q", got)
	}
}

// TestRespondTriggerRedirectsAPlainForm keeps the fallback identical
// across both helpers.
func TestRespondTriggerRedirectsAPlainForm(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/timezone", nil)
	rec := httptest.NewRecorder()

	respondTrigger(rec, req, "settingsUpdated", "/settings")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/settings" {
		t.Errorf("Location = %q, want /settings", got)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Errorf("a plain form response carried HX-Trigger: %q", got)
	}
}

// TestOnlyAnExactHeaderCountsAsHtmx pins the comparison the call sites
// used. htmx sends the literal string "true"; anything else is a plain
// navigation and has to be redirected.
func TestOnlyAnExactHeaderCountsAsHtmx(t *testing.T) {
	for _, value := range []string{"", "false", "TRUE", "1", "yes"} {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		if value != "" {
			req.Header.Set("HX-Request", value)
		}
		rec := httptest.NewRecorder()

		respondRefresh(rec, req, "/x")
		if rec.Code != http.StatusSeeOther {
			t.Errorf("HX-Request=%q produced %d, want a redirect", value, rec.Code)
		}
	}
}

// TestTheBlockIsNotWrittenOutByHand is the regression guard. The point
// of the extraction is that the contract lives in one place, so a new
// handler copying the old block back in has to fail.
//
// The VLAN handler is the one legitimate exception: it renders a partial
// for htmx instead of answering with a header, which is a different
// contract and not what these helpers express.
func TestTheBlockIsNotWrittenOutByHand(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || name == "respond.go" {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(raw)

		for _, header := range []string{`w.Header().Set("HX-Refresh"`, `w.Header().Set("HX-Trigger"`} {
			if strings.Contains(body, header) {
				t.Errorf("%s sets the htmx response header itself; use respondRefresh or respondTrigger", name)
			}
		}
		if name != "vlan.go" && strings.Contains(body, `r.Header.Get("HX-Request")`) {
			t.Errorf("%s writes the htmx branch out by hand", name)
		}
	}
}
