package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// startCappedServer runs a real agent with a connection cap a test can
// reach. Sixteen live connections would work too, but a small cap keeps
// the test cheap and makes the boundary explicit.
func startCappedServer(t *testing.T, maxConns int) string {
	t.Helper()

	sock := limitTestSocket(t)
	srv := NewServer(sock)
	srv.connSlots = make(chan struct{}, maxConns)
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
		c, err := net.Dial("unix", sock)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Dialling a Unix socket succeeds as soon as the kernel queues
		// the connection, which can be before the accept loop has even
		// seen it. Round-trip a request so the probe is known to have
		// been accepted, then wait for its slot to come back. Without
		// both steps the test's own first connection races the probe
		// for the last slot.
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write([]byte(pingFrame())); err != nil {
			_ = c.Close()
			time.Sleep(10 * time.Millisecond)
			continue
		}
		var resp Response
		if err := json.NewDecoder(c).Decode(&resp); err != nil {
			_ = c.Close()
			time.Sleep(10 * time.Millisecond)
			continue
		}
		_ = c.Close()

		for time.Now().Before(deadline) {
			if len(srv.connSlots) == 0 {
				return sock
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("the probe connection never released its slot")
	}
	t.Fatalf("agent socket %s never became connectable", sock)
	return ""
}

// liveConn opens a connection and proves the agent is serving it, so a
// caller holding several of them knows every slot is genuinely taken
// and is not racing the accept loop.
func liveConn(t *testing.T, sock string) net.Conn {
	t.Helper()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := conn.Write([]byte(pingFrame())); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	resp, err := readReply(t, conn, 3*time.Second)
	if err != nil {
		t.Fatalf("read ping reply: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("ping was refused: %v", resp.Error)
	}
	return conn
}

// TestConcurrentConnectionsAreBounded is the regression test. The
// accept loop spawned a goroutine per connection with no bound
// anywhere, and because the agent runs as root and a connection can
// fork a privileged command, the only thing holding real traffic to one
// command at a time was that the shipped client happens to use a single
// mutex-guarded connection.
func TestConcurrentConnectionsAreBounded(t *testing.T) {
	sock := startCappedServer(t, 2)

	liveConn(t, sock)
	liveConn(t, sock)

	extra, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = extra.Close() }()

	if _, err := extra.Write([]byte(pingFrame())); err != nil {
		// A write to a socket the agent already closed is itself a
		// refusal, which is the behaviour under test.
		return
	}

	resp, err := readReply(t, extra, 3*time.Second)
	if err != nil {
		t.Fatalf("the connection above the cap was served instead of refused: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("the connection above the cap got a successful reply")
	}
	if !strings.Contains(resp.Error.Message, "too many concurrent connections") {
		t.Errorf("unexpected refusal: %+v", resp.Error)
	}
}

// TestARefusedConnectionIsClosed keeps the refusal from leaking the
// very resource the cap protects.
func TestARefusedConnectionIsClosed(t *testing.T) {
	sock := startCappedServer(t, 1)

	liveConn(t, sock)

	extra, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = extra.Close() }()

	_ = extra.SetReadDeadline(time.Now().Add(3 * time.Second))
	r := bufio.NewReader(extra)

	// The refusal frame, then EOF.
	var resp Response
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		t.Fatalf("read refusal: %v", err)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "too many concurrent connections") {
		t.Fatalf("unexpected reply: %+v", resp)
	}

	if _, err := r.ReadByte(); err == nil {
		t.Error("the agent kept a refused connection open")
	}
}

// TestAClosedConnectionFreesItsSlot keeps the cap from being one-way.
// An agent that stops accepting after sixteen lifetime connections
// would take the web UI down with it.
func TestAClosedConnectionFreesItsSlot(t *testing.T) {
	sock := startCappedServer(t, 1)

	first, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := first.Write([]byte(pingFrame())); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if _, err := readReply(t, first, 3*time.Second); err != nil {
		t.Fatalf("read ping reply: %v", err)
	}
	_ = first.Close()

	// The slot is returned when the handler goroutine notices the
	// close, which is not instant.
	deadline := time.Now().Add(3 * time.Second)
	for {
		second, dialErr := net.Dial("unix", sock)
		if dialErr != nil {
			t.Fatalf("dial: %v", dialErr)
		}
		if _, err := second.Write([]byte(pingFrame())); err == nil {
			resp, readErr := readReply(t, second, time.Second)
			if readErr == nil && resp.Error == nil {
				_ = second.Close()
				return
			}
		}
		_ = second.Close()

		if time.Now().After(deadline) {
			t.Fatal("the closed connection never released its slot")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTheProductionServerCarriesTheCap pins the wiring, since every
// other test here builds a server with its own number.
func TestTheProductionServerCarriesTheCap(t *testing.T) {
	srv := NewServer("/run/lankeeper/agent.sock")

	if got := cap(srv.connSlots); got != defaultMaxConns {
		t.Errorf("connection cap = %d, want %d", got, defaultMaxConns)
	}
	if defaultMaxConns <= 0 {
		t.Error("a non-positive cap would refuse every connection")
	}
}
