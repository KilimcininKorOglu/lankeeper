package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// limitTestSocket keeps the path under the platform's sun_path limit,
// which is 104 bytes on macOS. A path built from the test name would
// overflow it and the listener would fail for an unrelated reason.
func limitTestSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// startLimitedServer runs a real agent with the frame limits tightened
// so a test does not have to send a mebibyte or wait half a minute.
func startLimitedServer(t *testing.T, maxBytes int64, timeout time.Duration) string {
	t.Helper()

	sock := limitTestSocket(t)
	srv := NewServer(sock)
	srv.maxFrameBytes = maxBytes
	srv.frameTimeout = timeout
	RegisterBuiltinOps(srv)

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
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent socket %s never became connectable", sock)
	return ""
}

func pingFrame() string {
	return `{"jsonrpc":"2.0","method":"ping","id":1}` + "\n"
}

// readReply reads one JSON object from conn, or reports why it could
// not. A closed connection returns an error, which is how the size and
// timeout rejections surface.
func readReply(t *testing.T, conn net.Conn, within time.Duration) (Response, error) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))

	var resp Response
	err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp)
	return resp, err
}

// TestOversizedFrameIsRejected covers the memory side. Params is a
// json.RawMessage, so without a cap the decoder buffers the whole value
// inside the root process before dispatch.
func TestOversizedFrameIsRejected(t *testing.T) {
	sock := startLimitedServer(t, 4<<10, 5*time.Second)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// One request whose params alone dwarf the 4 KiB limit.
	huge := strings.Repeat("A", 64<<10)

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	// The server closes mid-write, so a short write is the expected
	// outcome rather than an error to fail on.
	_, _ = fmt.Fprintf(conn,
		`{"jsonrpc":"2.0","method":"ping","params":{"x":%q},"id":1}`+"\n", huge)

	if _, err := readReply(t, conn, 5*time.Second); err == nil {
		t.Error("oversized frame was answered instead of rejected")
	}
}

// TestServerSurvivesAnOversizedFrame is the other half: the rejection
// must cost one connection, not the agent.
func TestServerSurvivesAnOversizedFrame(t *testing.T) {
	sock := startLimitedServer(t, 4<<10, 5*time.Second)

	bad, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	huge := strings.Repeat("A", 64<<10)
	_ = bad.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(bad,
		`{"jsonrpc":"2.0","method":"ping","params":{"x":%q},"id":1}`+"\n", huge)
	_ = bad.Close()

	good, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial after rejection: %v", err)
	}
	defer func() { _ = good.Close() }()

	if _, err := good.Write([]byte(pingFrame())); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := readReply(t, good, 5*time.Second)
	if err != nil {
		t.Fatalf("agent stopped serving after an oversized frame: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("ping returned an error: %v", resp.Error)
	}
}

// TestHalfSentFrameIsDroppedAtTheTimeout covers the goroutine side: a
// client that starts a frame and stalls used to hold its handler
// forever.
func TestHalfSentFrameIsDroppedAtTheTimeout(t *testing.T) {
	sock := startLimitedServer(t, 1<<20, 200*time.Millisecond)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// A frame that has begun but will never end.
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","method":"pi`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The server must close the connection. Asserting merely that the
	// read fails would pass without the fix, because this side's own
	// deadline also produces an error, so the distinction matters: a
	// timeout here means the handler is still parked on the frame.
	_, err = readReply(t, conn, 3*time.Second)
	if err == nil {
		t.Fatal("a half-sent frame was answered")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Errorf("client read timed out instead of the server closing: the handler is still held by the half-sent frame (%v)", err)
	}
}

// TestIdleConnectionIsNotClosed is the counterweight. The client keeps
// one connection open between calls, so a naive idle deadline would
// turn ordinary quiet periods into failed calls.
func TestIdleConnectionIsNotClosed(t *testing.T) {
	const frameTimeout = 150 * time.Millisecond
	sock := startLimitedServer(t, 1<<20, frameTimeout)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Sit idle for several frame timeouts without sending anything.
	time.Sleep(4 * frameTimeout)

	if _, err := conn.Write([]byte(pingFrame())); err != nil {
		t.Fatalf("write after idling: %v", err)
	}
	resp, err := readReply(t, conn, 5*time.Second)
	if err != nil {
		t.Fatalf("idle connection was closed by the frame timeout: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("ping returned an error: %v", resp.Error)
	}
}

// TestSequentialRequestsReuseTheConnection proves the byte budget
// really resets. Without a reset the second request on a connection
// would trip the size limit, which is how a lifetime-wide LimitReader
// would have failed.
func TestSequentialRequestsReuseTheConnection(t *testing.T) {
	// A limit only a few times the size of one ping frame: three
	// requests would exceed it if the budget accumulated.
	sock := startLimitedServer(t, 120, 5*time.Second)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	dec := json.NewDecoder(reader)

	for i := range 3 {
		if _, err := conn.Write([]byte(pingFrame())); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		var resp Response
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("request %d on a reused connection failed, budget did not reset: %v", i, err)
		}
		if resp.Error != nil {
			t.Fatalf("request %d returned an error: %v", i, resp.Error)
		}
	}
}
