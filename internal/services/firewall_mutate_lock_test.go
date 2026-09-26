package services

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// Apply renders the custom rules under s.mu while the IPv6 lease watcher
// runs it in the background. Rule edits must take the same lock: run
// under -race, this fails when a mutator writes the slice unlocked, and
// two concurrent removes must never index past the shortened slice.
func TestFirewallMutatorsHoldTheServiceLock(t *testing.T) {
	t.Setenv("LANKEEPER_DATA_DIR", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	svc, err := NewFirewallServiceFromFS(cfg, "{{ range .CustomInputRules }}{{ . }}{{ end }}")
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}
	t.Cleanup(svc.stopWatchdog)

	rule := config.FirewallRule{Name: "ssh", Enabled: true}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range 20 {
				if err := svc.AddRule(rule); err != nil {
					t.Errorf("add rule: %v", err)
				}
				_ = svc.RemoveRule(0)
			}
		})
	}
	wg.Go(func() {
		for range 40 {
			svc.mu.Lock()
			svc.buildCustomRules()
			svc.mu.Unlock()
		}
	})
	wg.Wait()
}
