package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoBrowserStorageIsUsed is the regression test. The theme was kept
// in localStorage and in a cookie at the same time, read back from
// localStorage first. Two copies of one setting is one place for them to
// disagree, and this project stores browser-side state in cookies.
func TestNoBrowserStorageIsUsed(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "static", "js")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read js dir: %v", err)
	}

	for _, e := range entries {
		// The vendored htmx bundle is not ours to edit.
		if e.IsDir() || e.Name() == "htmx.min.js" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, api := range []string{"localStorage", "sessionStorage"} {
			if strings.Contains(string(raw), api) {
				t.Errorf("%s uses %s; browser-side state belongs in a cookie", e.Name(), api)
			}
		}
	}
}

// TestTheThemeRoundTripsThroughTheCookie runs the shipped code to prove
// the remaining path works, rather than only that the forbidden one is
// gone.
func TestTheThemeRoundTripsThroughTheCookie(t *testing.T) {
	harness := `
globalThis.window = globalThis;
var applied = null;
var written = [];
globalThis.document = {
    documentElement: {
        setAttribute(name, value) { if (name === 'data-theme') applied = value; },
        getAttribute() { return applied; },
    },
    get cookie() { return 'theme=light'; },
    set cookie(v) { written.push(v); },
    getElementById() { return null; },
    addEventListener() {},
};
globalThis.addEventListener = function() {};
globalThis.matchMedia = function() { return { matches: false }; };
globalThis.setTimeout = function() {};

APP_JS_HERE

var startup = applied;
window.toggleTheme();
console.log(JSON.stringify({ startup: startup, after: applied, written: written }));
`
	out := runNode(t, strings.Replace(harness, "APP_JS_HERE", readAppJS(t), 1))

	if !strings.Contains(out, `"startup":"light"`) {
		t.Errorf("the theme was not restored from the cookie: %s", out)
	}
	if !strings.Contains(out, `"after":"dark"`) {
		t.Errorf("toggling did not switch the theme: %s", out)
	}
	if !strings.Contains(out, "theme=dark") {
		t.Errorf("the new theme was not written to a cookie: %s", out)
	}
	if !strings.Contains(out, "SameSite=Strict") || !strings.Contains(out, "Secure") {
		t.Errorf("the theme cookie lost its attributes: %s", out)
	}
}
