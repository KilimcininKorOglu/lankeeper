package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// cancelTestSocket keeps the path well under the platform's sun_path
// limit, which is 104 bytes on macOS. A path derived from the test name
// would overflow it and the listener would fail for the wrong reason.
func cancelTestSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// fakeAgentListener answers each request after replyDelay, echoing the
// method name back so a reply can be traced to the call that asked for
// it. A delay longer than the caller's context models a privileged
// command that outlives the request.
type fakeAgentListener struct {
	ln         net.Listener
	accepted   atomic.Int64
	replyDelay func(method string) time.Duration
}

func newFakeAgentListener(t *testing.T, sock string, delay func(string) time.Duration) *fakeAgentListener {
	t.Helper()

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeAgentListener{ln: ln, replyDelay: delay}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.accepted.Add(1)
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeAgentListener) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			return
		}
		time.Sleep(f.replyDelay(req.Method))
		resp := Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]string{"method": req.Method},
		}
		if err := enc.Encode(&resp); err != nil {
			return
		}
	}
}

func neverReply(string) time.Duration { return time.Hour }

// TestCallReturnsWhenContextIsCancelled is the regression test. Call
// never selected on ctx.Done, so a shutdown or a disconnected client
// could not stop privileged work: the caller stayed blocked until the
// socket deadline expired on its own.
func TestCallReturnsWhenContextIsCancelled(t *testing.T) {
	sock := cancelTestSocket(t)
	newFakeAgentListener(t, sock, neverReply)

	c := NewClient(sock)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := c.Call(ctx, "exec.run", map[string]string{"cmd": "nft"})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Call returned %v, want context.Canceled", err)
		}
		// The fallback deadline is minutes long, so returning quickly
		// proves cancellation did the work, not the deadline.
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("Call took %v to notice cancellation", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Call ignored cancellation and is still blocked")
	}
}

// TestCallRejectsAlreadyCancelledContext keeps an abandoned request
// from reaching the root agent at all.
func TestCallRejectsAlreadyCancelledContext(t *testing.T) {
	sock := cancelTestSocket(t)
	f := newFakeAgentListener(t, sock, neverReply)

	c := NewClient(sock)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.Call(ctx, "exec.run", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Call returned %v, want context.Canceled", err)
	}
	if got := f.accepted.Load(); got != 0 {
		t.Errorf("client opened %d connections for a cancelled context, want 0", got)
	}
}

// TestCancelledCallDoesNotPoisonTheNextOne is the case the connection
// teardown exists for. The stream is a sequential request/response pipe
// and Decode does not match on response ID, so a call abandoned while
// its reply is still in flight would hand that reply to whoever calls
// next.
//
// The first call is cancelled while the server is still sleeping. The
// second call must receive its own reply, not the late one.
//
// The first context is cancelled rather than given a deadline on
// purpose. A deadline was always enforced through the socket; plain
// cancellation was the case Call ignored, so this exercises the fix
// rather than pre-existing behaviour.
func TestCancelledCallDoesNotPoisonTheNextOne(t *testing.T) {
	sock := cancelTestSocket(t)
	newFakeAgentListener(t, sock, func(method string) time.Duration {
		if method == "slow" {
			return 300 * time.Millisecond
		}
		return 0
	})

	c := NewClient(sock)
	t.Cleanup(func() { _ = c.Close() })

	slowCtx, cancelSlow := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancelSlow()
	}()
	defer cancelSlow()
	if _, err := c.Call(slowCtx, "slow", nil); err == nil {
		t.Fatal("expected the abandoned call to fail")
	}

	// Give the server time to write the reply nobody is waiting for.
	time.Sleep(400 * time.Millisecond)

	raw, err := c.Call(context.Background(), "fast", nil)
	if err != nil {
		t.Fatalf("call after an abandoned one failed: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["method"] != "fast" {
		t.Errorf("second call received the reply to %q, not its own", result["method"])
	}
}
