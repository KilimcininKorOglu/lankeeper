package web_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/web"
)

// TestARefusalSaysWhenToRetry is the regression test. The limiter
// answered a throttled request with a bare 429 and no Retry-After, even
// though the bucket already held everything needed to compute one. A
// browser shows the operator the error and stops, but /metrics carries
// no session and exists for a scraper, and automation is in the same
// position: without the header, a refusal reads as "try again now".
func TestARefusalSaysWhenToRetry(t *testing.T) {
	rl := web.NewRateLimiter(30*time.Second, 1)
	wrapped := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	doRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = "10.10.10.40:12345"
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		return rec
	}

	if rec := doRequest(); rec.Code != http.StatusOK {
		t.Fatalf("the burst request was refused: %d", rec.Code)
	}

	rec := doRequest()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}

	header := rec.Header().Get("Retry-After")
	if header == "" {
		t.Fatal("the refusal carries no Retry-After")
	}
	secs, err := strconv.Atoi(header)
	if err != nil {
		t.Fatalf("Retry-After = %q, which is not the delta-seconds form: %v", header, err)
	}
	// One token costs a full refill interval, and the client just spent
	// its last one, so the answer has to be close to that interval and
	// never longer than it.
	if secs < 1 || secs > 30 {
		t.Errorf("Retry-After = %d, want between 1 and 30 for a 30s refill", secs)
	}
}

// TestTheWaitTracksTheBucket keeps the header honest rather than a fixed
// placeholder: a client that has waited most of the interval must be
// told the shorter remainder.
func TestTheWaitTracksTheBucket(t *testing.T) {
	rl := web.NewRateLimiter(10*time.Second, 1)
	const ip = "10.10.10.41"

	if !rl.Allow(ip) {
		t.Fatal("the burst request was refused")
	}

	first := retryAfterFor(t, rl, ip)
	time.Sleep(1200 * time.Millisecond)
	later := retryAfterFor(t, rl, ip)

	if later >= first {
		t.Errorf("the wait did not shrink after a pause: %d then %d", first, later)
	}
}

// TestASubSecondWaitIsRoundedUp covers the encoding. RFC 9110 defines
// delta-seconds as a non-negative integer, so truncating a 200ms wait
// would emit 0, telling the client to retry immediately, which is the
// opposite of what a refusal means.
func TestASubSecondWaitIsRoundedUp(t *testing.T) {
	rl := web.NewRateLimiter(200*time.Millisecond, 1)
	const ip = "10.10.10.42"

	if !rl.Allow(ip) {
		t.Fatal("the burst request was refused")
	}
	if got := retryAfterFor(t, rl, ip); got != 1 {
		t.Errorf("Retry-After = %d for a 200ms wait, want 1", got)
	}
}

// TestAnAllowedRequestCarriesNoRetryAfter keeps the header off the
// success path, where it would tell a client to back off for no reason.
func TestAnAllowedRequestCarriesNoRetryAfter(t *testing.T) {
	rl := web.NewRateLimiter(time.Second, 5)
	wrapped := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.10.10.43:12345"
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("an allowed request carries Retry-After: %q", got)
	}
}

// retryAfterFor drives one throttled request through the middleware and
// returns the header as an integer.
func retryAfterFor(t *testing.T, rl *web.RateLimiter, ip string) int {
	t.Helper()

	wrapped := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = ip + ":12345"
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected a throttled response, got %d", rec.Code)
	}
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After = %q: %v", rec.Header().Get("Retry-After"), err)
	}
	return secs
}
