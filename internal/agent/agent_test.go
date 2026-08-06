package agent_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/agent"
)

func waitForSocket(t *testing.T, sock string, errCh <-chan error) {
	t.Helper()
	for i := 0; i < 200; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				t.Skipf("server failed to start: %v", err)
			}
			return
		default:
		}
		conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never became ready after 2s", sock)
}

// shortSocketPath returns a socket path short enough for the platform's
// sun_path limit (104 bytes on darwin). Deriving the path from the test
// name overflows it and turns these tests into silent skips.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

func TestServerClientRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	srv := agent.NewServer(sock)
	agent.RegisterBuiltinOps(srv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx)
	}()

	waitForSocket(t, sock, errCh)

	client := agent.NewClient(sock)
	defer func() { _ = client.Close() }()

	raw, err := client.Call(ctx, "ping", nil)
	if err != nil {
		t.Fatalf("ping call failed: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if result["status"] != "pong" {
		t.Errorf("expected pong, got %q", result["status"])
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("server error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("server did not shut down in time")
	}
}

func TestMethodNotFound(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	srv := agent.NewServer(sock)
	agent.RegisterBuiltinOps(srv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, sock, errCh)

	client := agent.NewClient(sock)
	defer func() { _ = client.Close() }()

	_, err := client.Call(ctx, "nonexistent.method", nil)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestSocketCleanup(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	srv := agent.NewServer(sock)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, sock, errCh)

	if _, err := os.Stat(sock); os.IsNotExist(err) {
		t.Fatal("socket file should exist while server is running")
	}

	cancel()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv.Close()

	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("socket file should be cleaned up after Close()")
	}
}

// TestSocketIsNotWorldAccessible pins the privilege boundary. The
// socket used to be chmod 0666, which let any local account drive the
// root agent and invoke whitelisted commands such as chpasswd.
func TestSocketIsNotWorldAccessible(t *testing.T) {
	sock := shortSocketPath(t)

	srv := agent.NewServer(sock)
	agent.RegisterBuiltinOps(srv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, sock, errCh)

	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	mode := info.Mode().Perm()

	if mode&0o007 != 0 {
		t.Errorf("socket mode %o grants access to other; the agent runs as root", mode)
	}
	if mode&0o002 != 0 {
		t.Errorf("socket mode %o is world-writable", mode)
	}
}

// TestSocketRemainsUsableAfterRestriction guards against the tightened
// mode locking out the legitimate caller: the owning account must still
// complete a round trip.
func TestSocketRemainsUsableAfterRestriction(t *testing.T) {
	sock := shortSocketPath(t)

	srv := agent.NewServer(sock)
	agent.RegisterBuiltinOps(srv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, sock, errCh)

	client := agent.NewClient(sock)
	defer func() { _ = client.Close() }()

	if _, err := client.Call(ctx, "ping", nil); err != nil {
		t.Fatalf("owner could not reach the agent after hardening: %v", err)
	}
}

// TestUnknownServiceGroupFailsClosed covers the misconfigured install:
// with no resolvable service group the socket must stay owner-only
// rather than falling back to a permissive mode.
func TestUnknownServiceGroupFailsClosed(t *testing.T) {
	sock := shortSocketPath(t)

	srv := agent.NewServerWithIdentity(sock, "no-such-user-4f2a", "no-such-group-4f2a")
	agent.RegisterBuiltinOps(srv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, sock, errCh)

	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("socket mode %o, want 600 when the service group cannot be resolved", mode)
	}
}
