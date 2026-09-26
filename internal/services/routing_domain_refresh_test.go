package services

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// Each refresh re-adds the resolved addresses with a timeout longer than
// the refresh interval, so a set never empties between two refreshes.
func TestDomainElementsOutliveTheRefreshInterval(t *testing.T) {
	d, err := time.ParseDuration(domainElementTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if d <= domainRefreshInterval {
		t.Fatalf("element timeout %v does not outlive the %v refresh interval", d, domainRefreshInterval)
	}
}

func TestStartDomainRefreshStopsWithItsContext(t *testing.T) {
	svc := NewRoutingService(config.DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	svc.StartDomainRefresh(ctx, &wg)
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh loop did not stop with its context")
	}
}

// Serve is the only place the loop can be started on the shutdown
// context, and nothing else starts it.
func TestServeStartsTheDomainRefresh(t *testing.T) {
	src, err := os.ReadFile("../web/server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "s.routingSvc.StartDomainRefresh(ctx, &bg)") {
		t.Fatal("Serve does not start the routing domain refresh")
	}
}
