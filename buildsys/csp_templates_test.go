package buildsys_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The policy in SecurityHeaders is `script-src 'self'`: no
// 'unsafe-inline', no 'unsafe-eval'. Three things therefore never run in
// a browser, and all three fail silently, so the control just sits there
// doing nothing:
//
//   - an on* attribute, blocked as an inline script
//   - an inline <script> block, blocked the same way
//   - htmx's hx-on, which is compiled with new Function and needs
//     'unsafe-eval'
//
// The whole template tree was written with all three before this was
// noticed. These tests are the reason it cannot come back: nothing about
// a dead onclick shows up in a Go test, a lint run, or a page render.

var (
	inlineHandler = regexp.MustCompile(`\son[a-z]+\s*=\s*"`)
	inlineScript  = regexp.MustCompile(`<script(?:\s[^>]*)?>`)
	scriptWithSrc = regexp.MustCompile(`<script[^>]*\ssrc\s*=`)
	hxOnAttr      = regexp.MustCompile(`\shx-on[:\w-]*\s*=`)
)

func TestTemplatesCarryNoInlineEventHandler(t *testing.T) {
	forEachTemplate(t, func(path, body string) {
		for _, line := range splitLines(body) {
			if inlineHandler.MatchString(line.text) {
				t.Errorf("%s:%d has an inline event handler, which CSP blocks; use a data attribute and a delegated listener in web/static/js/ui.js\n\t%s",
					path, line.n, strings.TrimSpace(line.text))
			}
		}
	})
}

func TestTemplatesCarryNoInlineScriptBlock(t *testing.T) {
	forEachTemplate(t, func(path, body string) {
		for _, line := range splitLines(body) {
			if inlineScript.MatchString(line.text) && !scriptWithSrc.MatchString(line.text) {
				t.Errorf("%s:%d opens an inline <script> block, which CSP blocks; move it to a file under web/static/js/\n\t%s",
					path, line.n, strings.TrimSpace(line.text))
			}
		}
	})
}

func TestTemplatesCarryNoHxOnAttribute(t *testing.T) {
	forEachTemplate(t, func(path, body string) {
		for _, line := range splitLines(body) {
			if hxOnAttr.MatchString(line.text) {
				t.Errorf("%s:%d uses hx-on, which htmx compiles with new Function and CSP blocks; use data-reset-after-request / data-hide-after-request\n\t%s",
					path, line.n, strings.TrimSpace(line.text))
			}
		}
	})
}

// TestCSPStaysStrict pins the two source expressions that make the tests
// above meaningful. Adding either one back would make every dead handler
// work again, and would also re-open the injection surface the canvas
// and the data attributes were chosen to avoid.
func TestCSPStaysStrict(t *testing.T) {
	middleware := readAsset(t, "../internal/web/middleware.go")

	idx := strings.Index(middleware, "Content-Security-Policy")
	if idx < 0 {
		t.Fatal("no Content-Security-Policy header is set")
	}
	policy := middleware[idx:]
	if end := strings.Index(policy, "\n"); end > 0 {
		policy = policy[:end]
	}

	scriptSrc := sourceList(policy, "script-src")
	if scriptSrc == "" {
		t.Fatal("the policy has no script-src directive")
	}
	for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'"} {
		if strings.Contains(scriptSrc, forbidden) {
			t.Errorf("script-src now allows %s; the inline-handler guards above stop meaning anything", forbidden)
		}
	}
}

// sourceList returns the directive's value, stopping at the next
// semicolon. Reading the whole policy instead would match
// 'unsafe-inline' on style-src, which is allowed on purpose.
func sourceList(policy, directive string) string {
	i := strings.Index(policy, directive)
	if i < 0 {
		return ""
	}
	rest := policy[i+len(directive):]
	if j := strings.Index(rest, ";"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

type numberedLine struct {
	n    int
	text string
}

func splitLines(body string) []numberedLine {
	raw := strings.Split(body, "\n")
	out := make([]numberedLine, 0, len(raw))
	for i, s := range raw {
		out = append(out, numberedLine{n: i + 1, text: s})
	}
	return out
}

func forEachTemplate(t *testing.T, check func(path, body string)) {
	t.Helper()
	root := "../web/templates"
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		raw, readErr := os.ReadFile(path) // #nosec G304 -- path comes from the walk
		if readErr != nil {
			return readErr
		}
		check(path, string(raw))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
