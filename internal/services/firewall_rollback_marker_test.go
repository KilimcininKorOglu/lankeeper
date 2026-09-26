package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// After a watchdog rollback the edit is still in router.yaml. A
// background apply-and-confirm must not re-install and confirm it; only
// an operator confirmation clears the way again.
func TestBackgroundApplyRefusesARolledBackChange(t *testing.T) {
	t.Chdir("../..")
	t.Setenv("LANKEEPER_FIREWALL_STATE", filepath.Join(t.TempDir(), "firewall-pending.json"))
	t.Setenv("LANKEEPER_FIREWALL_STAGING", t.TempDir())
	netutil.SetAgentClient(&execLogAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc, err := NewFirewallService(config.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.stopWatchdog)

	ac := netutil.NewAtomicChangeWithSnapshot("firewall", "table inet filter {}\n")
	svc.mu.Lock()
	svc.change = ac
	svc.armWatchdog(ac, time.Millisecond)
	svc.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(svc.rolledBackPath()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the rollback left no record")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := svc.ApplyAndConfirm(context.Background()); !errors.Is(err, ErrRolledBack) {
		t.Fatalf("background apply after a rollback: %v, want ErrRolledBack", err)
	}

	// The operator applies and confirms on the firewall page.
	if err := svc.Apply(context.Background()); err != nil {
		t.Fatalf("operator apply: %v", err)
	}
	svc.Confirm()
	if err := svc.ApplyAndConfirm(context.Background()); err != nil {
		t.Fatalf("background apply after the operator confirmed: %v", err)
	}
}
