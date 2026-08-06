package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// observedDeadline records what the server-side handler actually saw,
// which is the only thing that matters here: the whole defect was that
// the caller's budget never survived the trip across the socket.
type observedDeadline struct {
	mu        sync.Mutex
	seen      bool
	hasDeadl  bool
	remaining time.Duration
}

func (o *observedDeadline) record(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = true
	if d, ok := ctx.Deadline(); ok {
		o.hasDeadl = true
		o.remaining = time.Until(d)
	}
}

func (o *observedDeadline) get() (seen, hasDeadline bool, remaining time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.seen, o.hasDeadl, o.remaining
}

// startProbeServer runs a real agent whose only method reports the
// deadline on the context it was handed.
func startProbeServer(t *testing.T) (string, *observedDeadline) {
	t.Helper()

	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")

	obs := &observedDeadline{}
	srv := NewServer(sock)
	srv.Register("probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		obs.record(ctx)
		return map[string]string{"status": "ok"}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return sock, obs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent socket never became connectable")
	return "", nil
}

// TestCallerDeadlineReachesTheHandler is the regression test. The
// request envelope had no field for a budget, so the handler context
// never carried a deadline and opExecRun always substituted its own
// short default, overruling any caller that had allowed more.
func TestCallerDeadlineReachesTheHandler(t *testing.T) {
	sock, obs := startProbeServer(t)

	c := NewClient(sock)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if _, err := c.Call(ctx, "probe", nil); err != nil {
		t.Fatalf("call: %v", err)
	}

	seen, hasDeadline, remaining := obs.get()
	if !seen {
		t.Fatal("handler never ran")
	}
	if !hasDeadline {
		t.Fatal("handler context carried no deadline; the caller's budget was dropped at the socket")
	}
	// Well above the agent's own 30 s default, which is the point: a
	// caller asking for minutes must not be cut down to seconds.
	if remaining <= 30*time.Second {
		t.Errorf("handler saw %v remaining, want more than the 30s default", remaining)
	}
	if remaining > 4*time.Minute {
		t.Errorf("handler saw %v remaining, more than the caller granted", remaining)
	}
}

// TestNoCallerDeadlineLeavesTheHandlerDefault keeps the change from
// forcing a budget on callers that never asked for one. opExecRun still
// needs to apply its own default in that case.
func TestNoCallerDeadlineLeavesTheHandlerDefault(t *testing.T) {
	sock, obs := startProbeServer(t)

	c := NewClient(sock)
	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.Call(context.Background(), "probe", nil); err != nil {
		t.Fatalf("call: %v", err)
	}

	seen, hasDeadline, remaining := obs.get()
	if !seen {
		t.Fatal("handler never ran")
	}
	if hasDeadline {
		t.Errorf("handler context carried a %v deadline the caller never set; "+
			"the client's liveness fallback must not be forwarded as a budget", remaining)
	}
}

// TestRequestedTimeoutIsClamped bounds what an authenticated but
// compromised peer can ask for. Exercised directly because producing a
// multi-hour deadline through a real client would mean waiting for it.
func TestRequestedTimeoutIsClamped(t *testing.T) {
	cases := []struct {
		name string
		ms   int64
		want time.Duration
	}{
		{"absent", 0, 0},
		{"negative", -5000, 0},
		{"ordinary", 90_000, 90 * time.Second},
		{"at the ceiling", maxRequestedTimeout.Milliseconds(), maxRequestedTimeout},
		{"beyond the ceiling", (24 * time.Hour).Milliseconds(), maxRequestedTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestedTimeout(tc.ms); got != tc.want {
				t.Errorf("requestedTimeout(%d) = %v, want %v", tc.ms, got, tc.want)
			}
		})
	}
}

// TestAbsurdTimeoutIsClampedOverTheWire checks the clamp where it
// actually matters, on a real request rather than on the helper alone.
func TestAbsurdTimeoutIsClampedOverTheWire(t *testing.T) {
	sock, obs := startProbeServer(t)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	req := Request{
		JSONRPC:   "2.0",
		Method:    "probe",
		ID:        1,
		TimeoutMS: (72 * time.Hour).Milliseconds(),
	}
	if err := json.NewEncoder(conn).Encode(&req); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	seen, hasDeadline, remaining := obs.get()
	if !seen || !hasDeadline {
		t.Fatal("handler ran without a deadline")
	}
	if remaining > maxRequestedTimeout {
		t.Errorf("handler saw %v remaining, above the %v ceiling", remaining, maxRequestedTimeout)
	}
}
