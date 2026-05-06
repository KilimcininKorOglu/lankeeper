package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// repoRoot walks up from the current working directory until it
// finds a go.mod, so VPN/OpenVPN render paths
// (`configs/sysconf/<x>.conf.tmpl`) resolve correctly regardless of
// where `go test` was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root from %s", dir)
		}
		dir = parent
	}
}

// TestVPNServerDownNoOpFromInitialState verifies that ServerDown on a
// fresh service (running == false) returns ErrVPNAlreadyStopped
// instead of issuing `wg-quick down wgs0`. Without this idempotency
// guarantee, a stop-after-stop double-click would invoke wg-quick on
// an interface that never came up.
func TestVPNServerDownNoOpFromInitialState(t *testing.T) {
	t.Chdir(repoRoot(t))
	cfg := config.DefaultConfig()
	svc := services.NewVPNService(cfg)
	if err := svc.ServerDown(context.Background()); !errors.Is(err, services.ErrVPNAlreadyStopped) {
		t.Fatalf("expected ErrVPNAlreadyStopped, got %v", err)
	}
}

// TestVPNServerUpRejectsDoubleStart drives the production exec path
// through fakeAgent. The first ServerUp must reach exec.run; the
// second must short-circuit with ErrVPNAlreadyRunning, leaving the
// total wg-quick invocation count at exactly one. Even if two HTMX
// clicks land in the same second, the kernel only sees a single
// `wg-quick up`.
func TestVPNServerUpRejectsDoubleStart(t *testing.T) {
	t.Chdir(repoRoot(t))
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	svc := services.NewVPNService(cfg)

	if err := svc.ServerUp(context.Background()); err != nil {
		t.Fatalf("first ServerUp: %v", err)
	}
	if err := svc.ServerUp(context.Background()); !errors.Is(err, services.ErrVPNAlreadyRunning) {
		t.Fatalf("second ServerUp: expected ErrVPNAlreadyRunning, got %v", err)
	}

	// Count wg-quick up invocations through the agent log. Exactly
	// one is the contract.
	count := 0
	for _, c := range agent.execCallsCopy() {
		if c.Cmd == "wg-quick" && len(c.Args) >= 2 && c.Args[0] == "up" && c.Args[1] == "wgs0" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 wg-quick up invocation, got %d", count)
	}

	// Stop -> down should now reach exec, and a second stop should
	// be a no-op.
	if err := svc.ServerDown(context.Background()); err != nil {
		t.Fatalf("ServerDown: %v", err)
	}
	if err := svc.ServerDown(context.Background()); !errors.Is(err, services.ErrVPNAlreadyStopped) {
		t.Fatalf("second ServerDown: expected ErrVPNAlreadyStopped, got %v", err)
	}
}

// TestOpenVPNServerStopNoOpFromInitialState mirrors the WireGuard
// idempotency check for the systemctl path.
func TestOpenVPNServerStopNoOpFromInitialState(t *testing.T) {
	t.Chdir(repoRoot(t))
	cfg := config.DefaultConfig()
	svc := services.NewOpenVPNService(cfg)
	if err := svc.ServerStop(context.Background()); !errors.Is(err, services.ErrOpenVPNAlreadyStopped) {
		t.Fatalf("expected ErrOpenVPNAlreadyStopped, got %v", err)
	}
}

// TestOpenVPNServerStartRejectsDoubleStart drives the systemctl path
// through fakeAgent. The first ServerStart must reach exec.run; the
// second must short-circuit with ErrOpenVPNAlreadyRunning.
func TestOpenVPNServerStartRejectsDoubleStart(t *testing.T) {
	t.Chdir(repoRoot(t))
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	svc := services.NewOpenVPNService(cfg)

	if err := svc.ServerStart(context.Background()); err != nil {
		t.Fatalf("first ServerStart: %v", err)
	}
	if err := svc.ServerStart(context.Background()); !errors.Is(err, services.ErrOpenVPNAlreadyRunning) {
		t.Fatalf("second ServerStart: expected ErrOpenVPNAlreadyRunning, got %v", err)
	}

	count := 0
	for _, c := range agent.execCallsCopy() {
		if c.Cmd == "systemctl" && len(c.Args) >= 2 && c.Args[0] == "start" && c.Args[1] == "openvpn@server" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 systemctl start, got %d", count)
	}
}
