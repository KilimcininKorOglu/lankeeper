package tmpl_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
	webfs "github.com/KilimcininKorOglu/lankeeper/web"
)

// A partial reached from two directions has to accept the same shape
// from both, and only one of those directions is checked anywhere else.
//
// RenderPartial hands the template a *PageData, so a partial written
// against the page-side dot reads its fields off the wrong value. Inside
// {{ with .Data }} the dot is the data map, and a missing key on a map
// yields no value silently, so the page path renders a plausible-looking
// fragment either way. The RenderPartial path does not: the same lookup
// against the PageData struct is a missing field, which is an execute
// error, and the operator gets an error page instead of the fragment.
//
// TestRendererParsesEmbeddedTemplates only parses. Parsing cannot see
// this, because which field a dot resolves to is decided at execute
// time. So the partial has to actually run here, against the shipped
// templates, with the value RenderPartial really passes.
func TestHealthcheckPartialRendersFromPageData(t *testing.T) {
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

	// Mirrors what HealthCheckHandler.HandleStatus builds, including the
	// unhealthy branch so the failure count and the reset control are
	// both exercised.
	data := &tmpl.PageData{
		Lang: "en",
		Page: "network",
		Data: map[string]any{
			"HealthChecks": map[string]any{
				"wan": map[string]any{
					"Status":       "fail",
					"FailureCount": 3,
					"LastAction":   "restartPPPoE",
				},
			},
		},
	}

	rec := httptest.NewRecorder()
	if err := r.RenderPartial(rec, "network", "healthcheck", data); err != nil {
		t.Fatalf("the shipped healthcheck partial does not execute against a *PageData: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"wan",
		"3",
		"restartPPPoE",
		`hx-post="/network/healthcheck/wan/reset"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered fragment is missing %q; got:\n%s", want, body)
		}
	}

	// $.Lang has to resolve through the same value, or every label in
	// the fragment silently falls back instead of translating.
	if strings.Contains(body, "<no value>") {
		t.Errorf("a lookup resolved to nothing, so the dot is not the value RenderPartial passes:\n%s", body)
	}
}
