package services

import (
	"context"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// The shipped schedule refreshes the blocklist daily; it has to reach
// UpdateBlocklist when due and not before.
func TestBlocklistRefreshesOnItsSchedule(t *testing.T) {
	agent := &fileWriteRecorder{files: map[string]string{}}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.DNS.BlocklistURLs = nil
	cfg.DNS.BlocklistUpdateSchedule = "0 3 * * *"
	svc := NewDNSService(cfg)

	before := time.Date(2026, 9, 26, 2, 59, 0, 0, time.Local)
	next, source := svc.blocklistTick(context.Background(), before, time.Time{}, "")
	if want := time.Date(2026, 9, 26, 3, 0, 0, 0, time.Local); !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
	if _, ran := agent.files["/etc/unbound/blocklist.conf"]; ran {
		t.Fatal("the blocklist was refreshed before it was due")
	}

	next, _ = svc.blocklistTick(context.Background(), next, next, source)
	if _, ran := agent.files["/etc/unbound/blocklist.conf"]; !ran {
		t.Fatal("the blocklist was not refreshed when due")
	}
	if want := time.Date(2026, 9, 27, 3, 0, 0, 0, time.Local); !next.Equal(want) {
		t.Errorf("next after a run = %v, want %v", next, want)
	}
}
