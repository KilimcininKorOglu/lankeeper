package web

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// captureLog redirects the standard logger for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	origOut := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	})
	return &buf
}

func serveThrough(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	RequestLogger(h).ServeHTTP(rec, req)
	return rec
}

// TestRequestLogCarriesTheStatusCode is the regression test. The logger
// passed the raw ResponseWriter straight through, so nothing observed
// what the handler wrote. From the log alone a 200 and a 500 were
// indistinguishable, and a bare http.Error or a 404 from an unmatched
// route produced no application line at all.
func TestRequestLogCarriesTheStatusCode(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{"explicit error", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		}, http.StatusInternalServerError},
		{"not found", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}, http.StatusNotFound},
		{"body without WriteHeader", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}, http.StatusOK},
		{"nothing written at all", func(http.ResponseWriter, *http.Request) {}, http.StatusOK},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dns", http.StatusSeeOther)
		}, http.StatusSeeOther},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			serveThrough(t, tc.handler, httptest.NewRequest(http.MethodGet, "/dns", nil))

			line := strings.TrimSpace(buf.String())
			if !regexp.MustCompile(`^GET /dns \d+ `).MatchString(line) {
				t.Fatalf("log line does not carry a status: %q", line)
			}
			if !strings.Contains(line, " "+itoa(tc.want)+" ") {
				t.Errorf("log line = %q, want status %d", line, tc.want)
			}
		})
	}
}

// TestRequestLogKeepsTheEscapedPath keeps the log-forging fix from being
// undone by the wrapper work.
func TestRequestLogKeepsTheEscapedPath(t *testing.T) {
	buf := captureLog(t)

	req := httptest.NewRequest(http.MethodGet, "/dns", nil)
	req.URL.Path = "/dns\r\nforged line"

	serveThrough(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), req)

	if strings.Contains(buf.String(), "\r\nforged") {
		t.Errorf("the path was logged undecoded: %q", buf.String())
	}
}

// TestStatusRecorderStillFlushes is the one that matters for the SSE
// endpoint: it asserts http.Flusher on the writer it is handed, so a
// wrapper that does not forward Flush turns every event stream into
// "streaming unsupported".
func TestStatusRecorderStillFlushes(t *testing.T) {
	var sawFlusher, flushed bool

	rec := httptest.NewRecorder()
	RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		sawFlusher = ok
		if ok {
			_, _ = w.Write([]byte("event: ping\n\n"))
			f.Flush()
			flushed = true
		}
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))

	if !sawFlusher {
		t.Fatal("the wrapped writer is not an http.Flusher; SSE would refuse to stream")
	}
	if !flushed {
		t.Error("Flush was not reached")
	}
}

// TestStatusRecorderUnwraps keeps http.ResponseController working, which
// reaches the underlying writer through Unwrap.
func TestStatusRecorderUnwraps(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: inner}

	if rec.Unwrap() != http.ResponseWriter(inner) {
		t.Error("Unwrap did not return the wrapped writer")
	}
}

// itoa avoids pulling strconv in for one call in a table.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
