package agent

import (
	"net"
	"testing"
	"time"
)

// The client's encoder ends every request with a newline, which can reach
// the reader on its own after the decoder has returned the request. It
// must not start a frame, or an idle connection is dropped when the
// frame deadline expires and the client's next call fails.
func TestInterFrameWhitespaceDoesNotArmTheDeadline(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()

	fr := &frameReader{conn: server, max: 1 << 16, timeout: time.Second}
	buf := make([]byte, 64)

	go func() { _, _ = client.Write([]byte("\n\r\t ")) }()
	if _, err := fr.Read(buf); err != nil {
		t.Fatalf("read whitespace: %v", err)
	}
	if fr.armed {
		t.Fatal("whitespace between frames armed the frame deadline")
	}

	go func() { _, _ = client.Write([]byte(`{"jsonrpc"`)) }()
	if _, err := fr.Read(buf); err != nil {
		t.Fatalf("read frame start: %v", err)
	}
	if !fr.armed {
		t.Fatal("the first byte of a frame did not arm the deadline")
	}
}
