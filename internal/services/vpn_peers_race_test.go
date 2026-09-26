package services

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// The invite GC compacted the peer list in place while the status page,
// /metrics and other services' Save read it without the VPN lock. Run
// under -race, this fails when a mutation writes into a backing array a
// reader may hold, or a reader skips the lock.
func TestPeerListReadersNeverSeeAnInPlaceMutation(t *testing.T) {
	netutil.SetAgentClient(&recordingAgent{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	svc := NewVPNService(cfg)
	expired := time.Now().Add(-time.Hour)

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			svc.mu.Lock()
			svc.cfg.VPN.Server.Peers = append(svc.cfg.VPN.Server.Peers,
				config.WGServerPeer{Name: "stale", Pending: true, InviteExpiresAt: expired},
				config.WGServerPeer{Name: "kept"})
			svc.mu.Unlock()
			svc.GCExpiredInvites()
			_ = svc.RemovePeer("kept")
		}
	})
	wg.Go(func() {
		for range 50 {
			if _, err := svc.ServerStatus(context.Background()); err != nil {
				t.Errorf("server status: %v", err)
			}
			for _, p := range svc.Peers() {
				_ = p.Name
			}
		}
	})
	wg.Wait()

	for _, p := range svc.Peers() {
		if p.Name == "stale" {
			t.Error("an expired invite survived the sweep")
		}
	}
}
