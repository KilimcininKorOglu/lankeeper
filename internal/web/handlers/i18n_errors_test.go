package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
)

// localeKeys reads one locale file. Tests run with the package directory
// as CWD, hence the relative path out to the repository root.
func localeKeys(t *testing.T, name string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "locales", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return m
}

// referencedErrorKeys collects every key the web tree asks for.
func referencedErrorKeys(t *testing.T) map[string][]string {
	t.Helper()
	pat := regexp.MustCompile(`(?:clientError|clientErrorf|httpErrorT)\([^)]*?"(error\.[A-Za-z0-9_.]+)"`)

	out := map[string][]string{}
	for _, dir := range []string{".", filepath.Join("..")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			p := filepath.Join(dir, name)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			for _, m := range pat.FindAllSubmatch(b, -1) {
				key := string(m[1])
				out[key] = append(out[key], p)
			}
		}
	}
	return out
}

// TestEveryErrorKeyExistsInBothLocales is the regression test. Error
// responses were English literals written straight into http.Error, so
// the project's own rule that every visible string resolves through the
// locale files held for pages but not for errors, which is most of what
// an operator sees when something goes wrong.
func TestEveryErrorKeyExistsInBothLocales(t *testing.T) {
	en := localeKeys(t, "en.json")
	tr := localeKeys(t, "tr.json")

	keys := referencedErrorKeys(t)
	if len(keys) < 50 {
		t.Fatalf("only %d error keys found; the scan is not seeing the handlers", len(keys))
	}

	for key, files := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("%q is used in %v but missing from en.json", key, files)
		}
		if _, ok := tr[key]; !ok {
			t.Errorf("%q is used in %v but missing from tr.json", key, files)
		}
	}
}

// TestLocaleFilesStayInSync keeps the pair from drifting, which is the
// failure mode that leaves one language showing raw keys.
func TestLocaleFilesStayInSync(t *testing.T) {
	en := localeKeys(t, "en.json")
	tr := localeKeys(t, "tr.json")

	for k := range en {
		if _, ok := tr[k]; !ok {
			t.Errorf("%q is in en.json but not tr.json", k)
		}
	}
	for k := range tr {
		if _, ok := en[k]; !ok {
			t.Errorf("%q is in tr.json but not en.json", k)
		}
	}
}

// TestTurkishErrorMessagesAreTranslated guards against a copy of the
// English text landing in tr.json, which would satisfy the key check
// while leaving the operator reading English.
func TestTurkishErrorMessagesAreTranslated(t *testing.T) {
	en := localeKeys(t, "en.json")
	tr := localeKeys(t, "tr.json")

	for key := range referencedErrorKeys(t) {
		e, ok1 := en[key]
		r, ok2 := tr[key]
		if !ok1 || !ok2 {
			continue
		}
		// A few strings are legitimately identical across languages
		// because they are acronyms or protocol names.
		if e == r && !strings.EqualFold(e, "CSRF") {
			switch key {
			case "error.invalidCIDR", "error.invalidMTU", "error.invalidJSON":
				// Contain only technical terms; still expected to differ.
			}
			t.Errorf("%q has the same text in both locales: %q", key, e)
		}
	}
}

// TestClientErrorResolvesThroughTheBundle covers the runtime half: the
// helper must return the localized message, not the key.
func TestClientErrorResolvesThroughTheBundle(t *testing.T) {
	bundle, err := i18n.New("en")
	if err != nil {
		t.Fatalf("new bundle: %v", err)
	}
	if err := bundle.LoadFromFS(os.DirFS(filepath.Join("..", "..", "..", "web")), "locales"); err != nil {
		t.Fatalf("load locales: %v", err)
	}

	orig := i18n.Default()
	i18n.SetDefault(bundle)
	t.Cleanup(func() { i18n.SetDefault(orig) })

	tr := localeKeys(t, "tr.json")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/firewall/rules", nil)
	req = req.WithContext(i18n.ContextWithLang(req.Context(), "tr"))

	clientError(rec, req, http.StatusBadRequest, "error.badForm")

	if got, want := strings.TrimSpace(rec.Body.String()), tr["error.badForm"]; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rec.Code)
	}
}
