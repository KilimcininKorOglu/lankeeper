package handlers

import (
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
)

// The probe badge is the one DNS-page message swapped into view, so it
// has to reach a Turkish operator in Turkish.
func TestProbeBadgeIsLocalized(t *testing.T) {
	bundle, err := i18n.New("en")
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.LoadFromFS(os.DirFS("../../../web"), "locales"); err != nil {
		t.Fatal(err)
	}
	prev := i18n.Default()
	i18n.SetDefault(bundle)
	t.Cleanup(func() { i18n.SetDefault(prev) })

	req := httptest.NewRequest("POST", "/dns/probe-dot", nil)
	req = req.WithContext(i18n.ContextWithLang(req.Context(), "tr"))

	rec := httptest.NewRecorder()
	writeProbeResult(rec, req, 0, errors.New("dial tcp: refused"))
	if body := rec.Body.String(); !strings.Contains(body, "Başarısız: dial tcp: refused") {
		t.Errorf("failure badge = %q", body)
	}

	rec = httptest.NewRecorder()
	writeProbeResult(rec, req, 42*time.Millisecond, nil)
	if body := rec.Body.String(); !strings.Contains(body, "Başarılı (42ms)") {
		t.Errorf("success badge = %q", body)
	}
}
