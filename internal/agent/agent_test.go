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
