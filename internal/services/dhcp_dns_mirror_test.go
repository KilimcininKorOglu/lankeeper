package services_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestStaticLeaseReachesUnbound is the regression test. The mirrored DNS
// record was only saved to router.yaml, and nothing re-rendered
// unbound.conf, so Unbound kept serving the old host record.
func TestStaticLeaseReachesUnbound(t *testing.T) {
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
	t.Chdir(repoRoot(t))

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.System.Domain = "lan"
	dhcp := services.NewDHCPService(cfg)
	dhcp.SetDNSService(services.NewDNSService(cfg))

	if err := dhcp.AddStaticLease("aa:bb:cc:dd:ee:ff", "10.10.10.50", "printer"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := dhcp.ApplyDNSMirror(context.Background()); err != nil {
		t.Fatalf("apply dns mirror: %v", err)
	}

	agent.mu.Lock()
	defer agent.mu.Unlock()
	for _, w := range agent.writeLog {
		if strings.HasSuffix(w.Path, "unbound.conf") && strings.Contains(w.Body, "printer.lan. IN A 10.10.10.50") {
			return
		}
	}
	t.Error("unbound.conf was not rewritten with the static lease record")
}
