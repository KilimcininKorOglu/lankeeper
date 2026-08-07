package web_test

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/web"
)

func TestRateLimiter(t *testing.T) {
	// One token back every 100ms, five spendable at once. The interval
	// is long enough that the six calls below cannot earn a token back
	// mid-test, so the sixth is denied on the burst alone.
	rl := web.NewRateLimiter(100*time.Millisecond, 5)

	for i := 0; i < 5; i++ {
		if !rl.Allow("192.168.1.1") {
			t.Errorf("request %d should be allowed (within burst)", i)
		}
	}

	if rl.Allow("192.168.1.1") {
		t.Error("request beyond burst should be denied")
	}

	if !rl.Allow("192.168.1.2") {
		t.Error("different IP should be allowed")
	}
}

// TestRateLimiterMiddlewareReturns429 guarantees that a denied
// request short-circuits with 429 Too Many Requests instead of
// reaching the wrapped handler. This is the contract the DoT-probe
// route relies on to cap goroutine occupancy: at burst+1 the
// blocking inner handler is never invoked, so the per-IP request
// rate caps the per-IP goroutine count.
func TestRateLimiterMiddlewareReturns429(t *testing.T) {
	rl := web.NewRateLimiter(time.Second, 2)
	calls := 0
	wrapped := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	doRequest := func() int {
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.RemoteAddr = "10.10.10.50:12345"
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 2; i++ {
		if code := doRequest(); code != http.StatusOK {
			t.Fatalf("burst request %d: code = %d, want 200", i, code)
		}
	}
	if code := doRequest(); code != http.StatusTooManyRequests {
		t.Fatalf("post-burst request: code = %d, want 429", code)
	}
	if calls != 2 {
		t.Fatalf("inner handler ran %d times, want 2 (burst limit)", calls)
	}
}

func TestLANOnlyMiddleware(t *testing.T) {
	_, lanNet, _ := net.ParseCIDR("10.10.10.0/24")
	middleware := web.LANOnly([]*net.IPNet{lanNet})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		remoteAddr string
		wantCode   int
	}{
		{"10.10.10.50:12345", http.StatusOK},
		{"10.10.10.1:8080", http.StatusOK},
		{"127.0.0.1:9999", http.StatusOK},
		{"8.8.8.8:443", http.StatusForbidden},
		{"192.168.1.1:80", http.StatusForbidden},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tt.remoteAddr
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != tt.wantCode {
			t.Errorf("LANOnly(%s) = %d, want %d", tt.remoteAddr, rec.Code, tt.wantCode)
		}
	}
}

// TestRequestLoggerDoesNotForgeLogLines drives a real TCP connection so
// the request target is percent-encoded exactly as an attacker would
// send it. net/url decodes %0d%0a into a literal CR LF in URL.Path, so
// logging Path let any client append arbitrary lines to the appliance
// log without credentials.
//
// log output is process-global, so no t.Parallel here.
func TestRequestLoggerDoesNotForgeLogLines(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	served := make(chan struct{})
	srv := httptest.NewServer(web.RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(served)
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = fmt.Fprint(conn,
		"GET /foo%0d%0aFAKE-LOG-LINE:%20injected HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if err != nil {
		t.Fatalf("write request: %v", err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("read response: %v", err)
	}
	<-served

	logged := buf.String()
	if strings.Contains(logged, "\nFAKE-LOG-LINE") {
		t.Errorf("attacker-controlled path forged a second log line: %q", logged)
	}
	if got := strings.Count(strings.TrimRight(logged, "\n"), "\n"); got != 0 {
		t.Errorf("one request produced %d extra log lines: %q", got, logged)
	}
	// The request must still be logged, in its on-the-wire form, so
	// the operator can see what was actually asked for.
	if !strings.Contains(logged, "/foo%0d%0aFAKE-LOG-LINE:%20injected") {
		t.Errorf("log line does not show the escaped request target: %q", logged)
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler := web.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("CSP header should be set")
	}
	// frame-ancestors 'none' must be present so CSP-conforming
	// browsers keep clickjacking protection even if they ignore the
	// (now-obsolete) X-Frame-Options header.
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors 'none', got %q", csp)
	}

	xcto := rec.Header().Get("X-Content-Type-Options")
	if xcto != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", xcto)
	}

	xfo := rec.Header().Get("X-Frame-Options")
	if xfo != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", xfo)
	}
}
