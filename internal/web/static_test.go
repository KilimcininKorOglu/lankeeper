package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testAssetFS() fstest.MapFS {
	return fstest.MapFS{
		"js/app.js":     &fstest.MapFile{Data: []byte("console.log('a')\n")},
		"css/reset.css": &fstest.MapFile{Data: []byte("body{margin:0}\n")},
	}
}

func getStatic(t *testing.T, h http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestStaticServesAValidatorAtAll is the regression this handler exists
// for. http.FileServer over an embed.FS sent no ETag and no
// Last-Modified, so the browser had nothing to revalidate against and
// refetched every asset on every page load.
func TestStaticServesAValidatorAtAll(t *testing.T) {
	h := newStaticHandler(testAssetFS())

	rec := getStatic(t, h, "/js/app.js", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("ETag"); got == "" {
		t.Error("no ETag on a static asset; the browser cannot revalidate")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control %q, want no-cache", got)
	}
}

// TestStaticETagIsQuotedAndContentDerived pins RFC 7232 quoting and the
// property that two different files cannot share a validator.
func TestStaticETagIsQuotedAndContentDerived(t *testing.T) {
	h := newStaticHandler(testAssetFS())

	js := getStatic(t, h, "/js/app.js", nil).Header().Get("ETag")
	css := getStatic(t, h, "/css/reset.css", nil).Header().Get("ETag")

	for _, tag := range []string{js, css} {
		if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' {
			t.Errorf("ETag %q is not a quoted-string as RFC 7232 requires", tag)
		}
	}
	if js == css {
		t.Error("two different assets share an ETag; it is not derived from content")
	}
}

// TestStaticETagIsStableAcrossRequests guards the failure mode where a
// validator is recomputed from something volatile: an ETag that changes
// per request never matches, so every revalidation misses and the cache
// is worse than none.
func TestStaticETagIsStableAcrossRequests(t *testing.T) {
	h := newStaticHandler(testAssetFS())

	first := getStatic(t, h, "/js/app.js", nil).Header().Get("ETag")
	second := getStatic(t, h, "/js/app.js", nil).Header().Get("ETag")

	if first != second {
		t.Errorf("ETag changed between requests: %q then %q", first, second)
	}
}

// TestStaticRevalidationReturns304 covers the whole point of the ETag:
// a matching conditional request must skip the body.
func TestStaticRevalidationReturns304(t *testing.T) {
	h := newStaticHandler(testAssetFS())
	etag := getStatic(t, h, "/js/app.js", nil).Header().Get("ETag")

	cases := map[string]string{
		"exact":     etag,
		"list":      `"wrong", ` + etag + `, "alsowrong"`,
		"wildcard":  "*",
		"weak form": "W/" + etag,
	}

	for name, ifNoneMatch := range cases {
		t.Run(name, func(t *testing.T) {
			rec := getStatic(t, h, "/js/app.js", map[string]string{"If-None-Match": ifNoneMatch})

			if rec.Code != http.StatusNotModified {
				t.Fatalf("status %d, want 304 for If-None-Match %q", rec.Code, ifNoneMatch)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("304 carried %d bytes of body", rec.Body.Len())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control %q on the 304, want no-cache; the directive must survive revalidation", got)
			}
		})
	}
}

// TestStaticNonMatchingValidatorSendsTheBody is the other half: a stale
// validator must not be answered 304.
func TestStaticNonMatchingValidatorSendsTheBody(t *testing.T) {
	h := newStaticHandler(testAssetFS())

	rec := getStatic(t, h, "/js/app.js", map[string]string{"If-None-Match": `"stale"`})

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 for a non-matching validator", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("200 carried no body")
	}
}

// TestStaticSendsNoLastModified pins the zero modTime. The only value
// available at startup is the process start time, which would claim
// every asset changed on every restart.
func TestStaticSendsNoLastModified(t *testing.T) {
	h := newStaticHandler(testAssetFS())

	if got := getStatic(t, h, "/js/app.js", nil).Header().Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified %q; a restart-derived date would falsely invalidate every asset", got)
	}
}

// TestStaticRejectsUnknownPaths keeps the handler a lookup against a
// fixed index rather than anything that resolves a caller-supplied path.
func TestStaticRejectsUnknownPaths(t *testing.T) {
	h := newStaticHandler(testAssetFS())

	for _, target := range []string{"/js/missing.js", "/../server.go", "/"} {
		if code := getStatic(t, h, target, nil).Code; code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, code)
		}
	}
}

// TestStaticHeadCarriesTheHeadersWithoutABody covers the method the
// manual handler would be most likely to get wrong.
func TestStaticHeadCarriesTheHeadersWithoutABody(t *testing.T) {
	h := newStaticHandler(testAssetFS())

	req := httptest.NewRequest(http.MethodHead, "/js/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("HEAD lost the ETag")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD carried %d bytes of body", rec.Body.Len())
	}
}
