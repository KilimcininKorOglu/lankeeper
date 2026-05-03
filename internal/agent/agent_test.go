package agent_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/home-router/internal/agent"
)

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

	for i := 0; i < 100; i++ {
		conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client := agent.NewClient(sock)
	defer client.Close()

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

	go srv.Serve(ctx)
	for i := 0; i < 100; i++ {
		conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client := agent.NewClient(sock)
	defer client.Close()

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
	go srv.Serve(ctx)
	for i := 0; i < 100; i++ {
		conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

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
