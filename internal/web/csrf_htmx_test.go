package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAnHTMXPostWithoutATokenIsRejected states the server-side half of
// the problem plainly: the middleware requires either the header or the
// form field, and an hx-post on a bare button sends neither.
//
// Every mutating control in the UI is an hx-post of that shape, so
// nothing about the token being correct matters if no request carries
// it.
func TestAnHTMXPostWithoutATokenIsRejected(t *testing.T) {
	wrapped := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// What htmx sends for `<button hx-post="/firewall/confirm">`: the
	// cookie the browser attaches, and nothing else.
	req := httptest.NewRequest(http.MethodPost, "/firewall/confirm", nil)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "0123456789abcdef"})
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; the premise of this test is stale", rec.Code)
	}
}

// TestTheFrontendSuppliesTheTokenOnEveryRequest is the regression test.
// The token cookie is deliberately not HttpOnly so client code can echo
// it into the header, but nothing did, and only the login and logout
// forms carry the hidden field. Every other mutating control is an
// hx-post that sent no token at all and was answered 403.
func TestTheFrontendSuppliesTheTokenOnEveryRequest(t *testing.T) {
	app := readAppJS(t)

	if !strings.Contains(app, "htmx:configRequest") {
		t.Error("nothing hooks htmx request configuration, so hx-post carries no token")
	}
	if !strings.Contains(app, "X-CSRF-Token") {
		t.Error("the CSRF header is never set on outgoing requests")
	}
	if !strings.Contains(app, "csrf_token") {
		t.Error("the token cookie is never read")
	}
}

// TestTheHeaderIsSetFromTheCookie runs the shipped listener rather than
// matching on its text, so a hook that reads the wrong cookie or sets
// the wrong header still fails.
func TestTheHeaderIsSetFromTheCookie(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	harness := `
globalThis.window = globalThis;
globalThis.document = {
    documentElement: { setAttribute() {}, getAttribute() { return 'dark'; } },
    cookie: 'theme=dark; csrf_token=` + token + `; other=x',
    getElementById() { return null; },
};
globalThis.localStorage = { getItem() { return null; }, setItem() {} };
globalThis.matchMedia = function() { return { matches: false }; };

var handlers = {};
globalThis.addEventListener = function(name, fn) { handlers[name] = fn; };
document.addEventListener = globalThis.addEventListener;
globalThis.setTimeout = function() {};

APP_JS_HERE

var hook = handlers['htmx:configRequest'];
if (!hook) { console.log('NO_HOOK'); process.exit(0); }
var evt = { detail: { headers: {}, verb: 'post' } };
hook(evt);
console.log(JSON.stringify(evt.detail.headers));
`
	script := strings.Replace(harness, "APP_JS_HERE", readAppJS(t), 1)

	out := runNode(t, script)
	if strings.Contains(out, "NO_HOOK") {
		t.Fatal("app.js registers no htmx:configRequest listener")
	}
	if !strings.Contains(out, `"X-CSRF-Token":"`+token+`"`) {
		t.Errorf("the listener did not set the header from the cookie: %s", out)
	}
}

// TestTheHeaderIsSkippedOnSafeMethods keeps the token from travelling on
// requests that do not need it. The server issues it on GET, so sending
// it back there only widens where the value goes.
func TestTheHeaderIsSkippedOnSafeMethods(t *testing.T) {
	for _, verb := range []string{"get", "head"} {
		out := runCSRFHook(t, "csrf_token=abc123", verb)
		if strings.Contains(out, "X-CSRF-Token") {
			t.Errorf("%s carried the token: %s", verb, out)
		}
	}
}

// TestNoHeaderWithoutACookie avoids sending an empty header, which would
// look like a submitted-but-wrong token rather than an absent one.
func TestNoHeaderWithoutACookie(t *testing.T) {
	out := runCSRFHook(t, "theme=dark", "post")
	if strings.Contains(out, "X-CSRF-Token") {
		t.Errorf("a header was set with no cookie present: %s", out)
	}
}

// runCSRFHook loads the shipped app.js under a minimal DOM stand-in and
// fires one htmx:configRequest, returning the resulting headers as JSON.
func runCSRFHook(t *testing.T, cookies, verb string) string {
	t.Helper()

	harness := `
globalThis.window = globalThis;
globalThis.document = {
    documentElement: { setAttribute() {}, getAttribute() { return 'dark'; } },
    cookie: ` + jsString(cookies) + `,
    getElementById() { return null; },
};
globalThis.localStorage = { getItem() { return null; }, setItem() {} };
globalThis.matchMedia = function() { return { matches: false }; };

var handlers = {};
globalThis.addEventListener = function(name, fn) { handlers[name] = fn; };
document.addEventListener = globalThis.addEventListener;
globalThis.setTimeout = function() {};

APP_JS_HERE

var hook = handlers['htmx:configRequest'];
if (!hook) { console.log('NO_HOOK'); process.exit(0); }
var evt = { detail: { headers: {}, verb: ` + jsString(verb) + ` } };
hook(evt);
console.log(JSON.stringify(evt.detail.headers));
`
	out := runNode(t, strings.Replace(harness, "APP_JS_HERE", readAppJS(t), 1))
	if strings.Contains(out, "NO_HOOK") {
		t.Fatal("app.js registers no htmx:configRequest listener")
	}
	return out
}

// jsString renders a Go string as a JavaScript literal.
func jsString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func readAppJS(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "js", "app.js"))
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	return string(raw)
}
