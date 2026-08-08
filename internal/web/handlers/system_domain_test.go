package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// postHostname drives the settings handler with a form body and returns
// the recorder plus the config it was given, so a test can see both the
// response and whether the value was stored.
func postHostname(t *testing.T, hostname, domain string) (*httptest.ResponseRecorder, *config.Config) {
	t.Helper()

	cfg := &config.Config{}
	cfg.System.Hostname = "hermes"
	cfg.System.Domain = "lan"

	h := NewSystemHandler(nil, cfg, nil, nil, nil, nil, services.NewSystemService(), nil)

	form := url.Values{"hostname": {hostname}, "domain": {domain}}
	req := httptest.NewRequest(http.MethodPost, "/settings/hostname",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	h.HandleUpdateHostname(rec, req)
	return rec, cfg
}

// TestTheHandlerRefusesADomainCarryingADirective ties the validator to
// the request path. The field used to be assigned straight from the
// form, so the rendered dnsmasq configuration took whatever was posted.
//
// The assertion that matters is the second one: a refused request must
// leave the stored domain untouched, because the handler mutates the
// config in place before it ever reaches SaveToFile.
func TestTheHandlerRefusesADomainCarryingADirective(t *testing.T) {
	rec, cfg := postHostname(t, "hermes", "lan\ndhcp-script=/tmp/evil.sh")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if cfg.System.Domain != "lan" {
		t.Errorf("the rejected domain was stored anyway: %q", cfg.System.Domain)
	}
	if cfg.System.Hostname != "hermes" {
		t.Errorf("a rejected request changed the hostname to %q", cfg.System.Hostname)
	}
}

// TestTheHandlerStillAcceptsARealDomain keeps the guard from blocking
// the ordinary case.
func TestTheHandlerStillAcceptsARealDomain(t *testing.T) {
	_, cfg := postHostname(t, "hermes", "home.arpa")

	if cfg.System.Domain != "home.arpa" {
		t.Errorf("domain = %q, want home.arpa", cfg.System.Domain)
	}
}

// TestAnOmittedDomainIsLeftAlone covers the empty-field path. The form
// treats "" as "no change", which is why ValidateDomain refuses an
// empty string and the handler checks for it separately.
func TestAnOmittedDomainIsLeftAlone(t *testing.T) {
	_, cfg := postHostname(t, "hermes", "")

	if cfg.System.Domain != "lan" {
		t.Errorf("an omitted domain changed the stored value to %q", cfg.System.Domain)
	}
}
