package agent

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// A client whose context ends closes its connection and redials. The
// command it abandoned must stop too, or it runs as root to its own
// timeout beside the retry.
func TestPeerDisconnectCancelsTheHandler(t *testing.T) {
	sock := limitTestSocket(t)
	srv := NewServer(sock)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	srv.Register("test.block", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-time.After(10 * time.Second):
		}
		return nil, ctx.Err()
	})

	go func() { _ = srv.Serve(t.Context()) }()

	conn := dialWhenReady(t, sock)
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","method":"test.block","id":1}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never started")
	}
	_ = conn.Close()

	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("closing the connection did not cancel the handler")
	}
}

// dialWhenReady dials sock until the server is listening.
func dialWhenReady(t *testing.T, sock string) net.Conn {
	t.Helper()
	var err error
	for range 300 {
		var conn net.Conn
		if conn, err = net.Dial("unix", sock); err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("dial: %v", err)
	return nil
}
