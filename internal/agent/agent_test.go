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
	for range 200 {
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

// waitForSettledSocketMode blocks until the agent has finished
// restricting the socket, and returns the mode it settled on.
//
// A successful dial is not that signal. net.Listen creates the socket
// file, and only the next statement restricts it, so a dial succeeds
// while the mode is still whatever the umask allowed - 0755 under the
// usual 022. Any test that stats straight after waitForSocket is racing
// that chmod, which is why it passes on a developer machine and fails
// on a loaded CI runner. restrictSocket settles on 0660 when it can
// hand the socket to the service group and 0600 when it cannot, so
// reaching either value means the boundary is in place.
func waitForSettledSocketMode(t *testing.T, sock string) os.FileMode {
	t.Helper()
	for range 200 {
		info, err := os.Stat(sock)
		if err == nil {
			switch mode := info.Mode().Perm(); mode {
			case 0o600, 0o660:
				return mode
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	t.Fatalf("socket mode never settled after 2s; last seen %o", info.Mode().Perm())
	return 0
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

	ctx := t.Context()

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
	for range 100 {
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

	ctx := t.Context()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, sock, errCh)

	mode := waitForSettledSocketMode(t, sock)

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

	ctx := t.Context()

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

	ctx := t.Context()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	waitForSocket(t, sock, errCh)

	if mode := waitForSettledSocketMode(t, sock); mode != 0o600 {
		t.Errorf("socket mode %o, want 600 when the service group cannot be resolved", mode)
	}
}
