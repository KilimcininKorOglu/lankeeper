package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSSEOutlivesTheServerWriteTimeout is the regression test. The
// server-wide WriteTimeout ended every event stream after 30 s. Here the
// timeout is 200 ms and the stream must still deliver keep-alives well
// past it.
func TestSSEOutlivesTheServerWriteTimeout(t *testing.T) {
	b := NewSSEBroker()
	b.keepAlive = 50 * time.Millisecond

	srv := httptest.NewUnstartedServer(b)
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	deadline := time.Now().Add(800 * time.Millisecond)
	sc := bufio.NewScanner(resp.Body)
	frames := 0
	for time.Now().Before(deadline) && sc.Scan() {
		if strings.HasPrefix(sc.Text(), ":") {
			frames++
		}
	}
	if time.Now().Before(deadline) {
		t.Fatalf("the stream ended after %d keep-alive frames, before the test window closed: %v", frames, sc.Err())
	}
}
