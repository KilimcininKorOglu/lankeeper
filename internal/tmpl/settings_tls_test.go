package tmpl_test

import (
	"maps"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
	webfs "github.com/KilimcininKorOglu/lankeeper/web"
)

func newTestRenderer(t *testing.T) *tmpl.Renderer {
	t.Helper()
	loc, err := i18n.New("en")
	if err != nil {
		t.Fatalf("init i18n: %v", err)
	}
	if err := loc.LoadFromFS(webfs.EmbeddedFS, "locales"); err != nil {
		t.Fatalf("load locales: %v", err)
	}
	r, err := tmpl.NewRenderer(webfs.EmbeddedFS, loc)
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}
	return r
}

func settingsData(extra map[string]any) *tmpl.PageData {
	fields := map[string]any{
		"Hostname":       "hermes",
		"Domain":         "lan",
		"FQDN":           "hermes.lan",
		"Timezone":       "Europe/Istanbul",
		"Language":       "en",
		"TLSMode":        "self-signed",
		"TLSSelfSigned":  config.SelfSignedConfig{},
		"TLSMkcert":      config.MkcertConfig{},
		"TLSACME":        config.ACMEConfig{},
		"TLSACMEStaging": true,
		"Version":        map[string]any{},
		"PendingUpdate":  false,
		"PendingVersion": "",
	}
	maps.Copy(fields, extra)
	return &tmpl.PageData{Lang: "en", Page: "settings", Data: fields}
}

// The certificate state on the settings page comes from a struct, not
// the map every other field uses, so the field names in the template
// have to match the Go type exactly. Parsing cannot catch a rename:
// which field a dot resolves to is decided at execute time.
func TestSettingsPageRendersCertificateDetail(t *testing.T) {
	r := newTestRenderer(t)

	notAfter := time.Date(2030, 4, 1, 12, 0, 0, 0, time.UTC)
	data := settingsData(map[string]any{
		"TLSSelfSigned": config.SelfSignedConfig{
			CN: "hermes.lan", ValidDays: 365, SANs: []string{"hermes.lan", "10.10.10.1"},
		},
		"TLSCert": &config.TLSCertInfo{
			Issuer:    "hermes.lan",
			NotBefore: notAfter.AddDate(-1, 0, 0),
			NotAfter:  notAfter,
			SANs:      []string{"hermes.lan", "10.10.10.1"},
		},
	})

	rec := httptest.NewRecorder()
	if err := r.Render(rec, "settings", "base", data); err != nil {
		t.Fatalf("settings page does not execute: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"2030-04-01",
		"hermes.lan, 10.10.10.1",
		`hx-post="/settings/tls/regenerate"`,
		`name="cn"`,
		`name="sans"`,
		`name="validDays"`,
		`value="365"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered settings page is missing %q", want)
		}
	}
	if strings.Contains(body, "<no value>") {
		t.Error("a lookup on the settings page resolved to nothing")
	}
}

// The page has to render when there is no certificate, because that is
// exactly when the operator comes looking. Failing the render here
// would leave them with an error page and no control to fix it.
func TestSettingsPageRendersWithoutACertificate(t *testing.T) {
	r := newTestRenderer(t)

	data := settingsData(map[string]any{"TLSCertError": "read cert: no such file"})

	rec := httptest.NewRecorder()
	if err := r.Render(rec, "settings", "base", data); err != nil {
		t.Fatalf("settings page does not execute without a certificate: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "No readable certificate") {
		t.Error("the page does not tell the operator the certificate is missing")
	}
	if !strings.Contains(body, `hx-post="/settings/tls/regenerate"`) {
		t.Error("the regenerate control is absent, so there is no way out of the missing-certificate state")
	}
	// The internal error string names a filesystem path. It belongs in
	// the log, not in a page served to the browser.
	if strings.Contains(body, "no such file") {
		t.Error("the raw error text reached the page")
	}
}

// The form is only meaningful in self-signed mode. Offering it under
// mkcert or ACME would invite the operator to overwrite a certificate
// this code did not issue and cannot reissue.
func TestSettingsPageHidesRegenerateOutsideSelfSigned(t *testing.T) {
	r := newTestRenderer(t)

	for _, mode := range []string{"mkcert", "acme"} {
		t.Run(mode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			data := settingsData(map[string]any{"TLSMode": mode})
			if err := r.Render(rec, "settings", "base", data); err != nil {
				t.Fatalf("settings page does not execute in %s mode: %v", mode, err)
			}
			if strings.Contains(rec.Body.String(), `hx-post="/settings/tls/regenerate"`) {
				t.Errorf("the regenerate form is offered in %s mode", mode)
			}
		})
	}
}

// The mode form is the only way back from mkcert, and the CA download is
// the only way the operator makes their devices trust it. Both have to
// be present and both have to hang off the mode the page is showing.
func TestSettingsPageOffersModeSwitchAndCADownload(t *testing.T) {
	r := newTestRenderer(t)

	rec := httptest.NewRecorder()
	data := settingsData(map[string]any{
		"TLSMode":   "mkcert",
		"TLSMkcert": config.MkcertConfig{SANs: []string{"hermes.lan", "10.10.10.1"}},
	})
	if err := r.Render(rec, "settings", "base", data); err != nil {
		t.Fatalf("settings page does not execute in mkcert mode: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`hx-post="/settings/tls/mode"`,
		`href="/settings/tls/ca"`,
		`value="mkcert"`,
		"hermes.lan, 10.10.10.1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mkcert-mode settings page is missing %q", want)
		}
	}
	if strings.Contains(body, "<no value>") {
		t.Error("a lookup on the settings page resolved to nothing")
	}
}

// The CA link is meaningless outside mkcert mode: there is no local CA
// to install, and offering the download would hand back a 404.
func TestSettingsPageHidesCADownloadOutsideMkcert(t *testing.T) {
	r := newTestRenderer(t)

	rec := httptest.NewRecorder()
	if err := r.Render(rec, "settings", "base", settingsData(nil)); err != nil {
		t.Fatalf("settings page does not execute: %v", err)
	}
	if strings.Contains(rec.Body.String(), `href="/settings/tls/ca"`) {
		t.Error("the CA download is offered in self-signed mode")
	}
}

// The ACME form is the only way to request a public certificate, and the
// staging badge is the only signal telling the operator the certificate
// they just got is not trusted by anything.
func TestSettingsPageOffersACMEConfiguration(t *testing.T) {
	r := newTestRenderer(t)

	rec := httptest.NewRecorder()
	data := settingsData(map[string]any{
		"TLSMode": "acme",
		"TLSACME": config.ACMEConfig{
			Enabled: true, Domain: "router.example.com", Email: "ops@example.com",
			DNSChallenge: config.DNSChallengeConfig{Provider: "cloudflare"},
		},
		"TLSACMEStaging": true,
	})
	if err := r.Render(rec, "settings", "base", data); err != nil {
		t.Fatalf("settings page does not execute in acme mode: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`hx-post="/settings/tls/acme"`,
		`name="domain"`,
		`name="email"`,
		`name="provider"`,
		`name="apiToken"`,
		"router.example.com",
		"Staging",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("acme settings page is missing %q", want)
		}
	}
	if strings.Contains(body, "<no value>") {
		t.Error("a lookup on the settings page resolved to nothing")
	}
}

// The stored token must never be echoed into the form. It is a live
// credential for the operator's whole DNS zone, and a page that renders
// it puts it in the browser cache and in any screen share.
func TestSettingsPageDoesNotEchoTheDNSToken(t *testing.T) {
	r := newTestRenderer(t)

	rec := httptest.NewRecorder()
	data := settingsData(map[string]any{
		"TLSMode": "acme",
		"TLSACME": config.ACMEConfig{
			Domain: "router.example.com", Email: "ops@example.com",
			DNSChallenge: config.DNSChallengeConfig{Provider: "cloudflare", APIToken: "cf-secret-token"},
		},
	})
	if err := r.Render(rec, "settings", "base", data); err != nil {
		t.Fatalf("settings page does not execute: %v", err)
	}
	if strings.Contains(rec.Body.String(), "cf-secret-token") {
		t.Error("the stored DNS API token was rendered into the page")
	}
}
