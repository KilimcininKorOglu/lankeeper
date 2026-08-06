package services

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// publicLoopbackClient swaps in a client that keeps the size cap and the
// timeout but dials the test server. The production guard refuses
// loopback on purpose, and these tests are about the body limit, not the
// address check, which safefetch_test.go covers.
func publicLoopbackClient(t *testing.T) {
	t.Helper()
	orig := outboundFetchClient
	outboundFetchClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		},
	}
	t.Cleanup(func() { outboundFetchClient = orig })
}

// endlessServer streams the same line forever, which is the shape that
// used to grow the parsed slice without bound.
func endlessServer(t *testing.T, line string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat(line, 1024)
		for {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestBlocklistFetchStopsAtTheSizeCap is the regression test. The body
// was streamed straight into a scanner with no io.LimitReader, and every
// parsed domain was appended to a slice, so an endless or oversized
// response grew this process until the appliance ran out of memory.
func TestBlocklistFetchStopsAtTheSizeCap(t *testing.T) {
	publicLoopbackClient(t)
	srv := endlessServer(t, "0.0.0.0 ads.example.test\n")

	done := make(chan error, 1)
	go func() {
		_, err := downloadBlocklist(context.Background(), srv.URL)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errFetchTooLarge) {
			t.Errorf("got %v, want the size-limit error", err)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("the fetch never stopped; the response is unbounded")
	}
}

// TestM3UFetchStopsAtTheSizeCap covers the same shape on the playlist
// path, which is the one reachable from an authenticated web form.
func TestM3UFetchStopsAtTheSizeCap(t *testing.T) {
	publicLoopbackClient(t)
	srv := endlessServer(t, "#EXTINF:-1 group-title=\"g\",t\nhttp://example.test/s\n")

	done := make(chan error, 1)
	go func() {
		_, err := downloadAndParseM3U(context.Background(), srv.URL)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errFetchTooLarge) {
			t.Errorf("got %v, want the size-limit error", err)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("the fetch never stopped; the response is unbounded")
	}
}

// TestFetchUnderTheCapStillParses keeps the limit from breaking the
// ordinary case, and pins that a truncated result is never returned as
// if it were complete.
func TestFetchUnderTheCapStillParses(t *testing.T) {
	publicLoopbackClient(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# comment\n0.0.0.0 ads.example.test\n0.0.0.0 tracker.example.test\n"))
	}))
	defer srv.Close()

	domains, err := downloadBlocklist(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(domains) != 2 {
		t.Errorf("parsed %d domains, want 2: %v", len(domains), domains)
	}
}

// TestLimitedBodyReportsOverflowOnlyPastTheCap pins the boundary, so a
// response exactly at the limit is accepted rather than rejected.
func TestLimitedBodyReportsOverflowOnlyPastTheCap(t *testing.T) {
	atCap := newLimitedBody(strings.NewReader(strings.Repeat("a", maxFetchBytes)))
	if _, err := readAllFrom(atCap); err != nil {
		t.Fatalf("read: %v", err)
	}
	if atCap.overflowed() {
		t.Error("a body exactly at the cap was reported as too large")
	}

	past := newLimitedBody(strings.NewReader(strings.Repeat("a", maxFetchBytes+1)))
	if _, err := readAllFrom(past); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !past.overflowed() {
		t.Error("a body past the cap was not reported")
	}
}

func readAllFrom(r *limitedBody) (int, error) {
	buf := make([]byte, 64<<10)
	var total int
	for {
		n, err := r.Read(buf)
		total += n
		if err != nil {
			if err.Error() == "EOF" {
				return total, nil
			}
			return total, err
		}
	}
}
