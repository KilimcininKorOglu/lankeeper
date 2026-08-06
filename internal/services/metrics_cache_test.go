package services

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// scrapeAgent counts the privileged commands a scrape causes. The real
// agent serialises every call behind one connection, which is why the
// count is the thing worth asserting on.
type scrapeAgent struct {
	mu    sync.Mutex
	calls int
}

func (a *scrapeAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	if method == "exec.run" {
		a.mu.Lock()
		a.calls++
		a.mu.Unlock()
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func (a *scrapeAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// newScrapeTestMetrics wires the two contributors that actually shell
// out, which is what makes an unauthenticated scrape expensive.
func newScrapeTestMetrics(t *testing.T) (*MetricsService, *scrapeAgent) {
	t.Helper()
	agent := &scrapeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	svc := NewMetricsService(cfg, nil, NewDNSService(cfg), nil, nil, nil, nil, nil, nil)
	return svc, agent
}

// TestSnapshotIsCachedBetweenScrapes is the regression test. /metrics
// carries no authentication, and every scrape ran the collectors live,
// forking privileged subprocesses through the root agent. Since all
// agent traffic serialises behind one mutex-guarded connection, any LAN
// device could scrape in a loop and delay the operator's own privileged
// operations.
//
// Mutates the process-global agent client, so no t.Parallel here.
func TestSnapshotIsCachedBetweenScrapes(t *testing.T) {
	svc, agent := newScrapeTestMetrics(t)
	ctx := context.Background()

	svc.Snapshot(ctx)
	first := agent.count()
	if first == 0 {
		t.Fatal("the first scrape ran no privileged commands; the test proves nothing")
	}

	for range 50 {
		svc.Snapshot(ctx)
	}
	if got := agent.count(); got != first {
		t.Errorf("50 further scrapes ran %d extra privileged commands, want 0", got-first)
	}
}

// TestSnapshotRefreshesAfterTheTTL keeps the cache from going stale,
// which would make the endpoint useless.
func TestSnapshotRefreshesAfterTheTTL(t *testing.T) {
	svc, agent := newScrapeTestMetrics(t)
	svc.cacheTTL = 20 * time.Millisecond
	ctx := context.Background()

	svc.Snapshot(ctx)
	first := agent.count()

	time.Sleep(40 * time.Millisecond)
	svc.Snapshot(ctx)

	if got := agent.count(); got <= first {
		t.Errorf("the snapshot was not refreshed after the TTL (%d commands before, %d after)", first, got)
	}
}

// TestConcurrentScrapesCollectOnce covers the coalescing half: a burst
// of simultaneous requests must not each start their own collection.
func TestConcurrentScrapesCollectOnce(t *testing.T) {
	svc, agent := newScrapeTestMetrics(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.Snapshot(ctx)
		}()
	}
	wg.Wait()

	// One collection's worth of commands, not 32.
	svc.cacheMu.Lock()
	cached := !svc.cachedAt.IsZero()
	svc.cacheMu.Unlock()
	if !cached {
		t.Fatal("no snapshot was cached")
	}

	single, _ := newScrapeTestMetrics(t)
	agent2 := &scrapeAgent{}
	netutil.SetAgentClient(agent2)
	single.Snapshot(ctx)

	if got, want := agent.count(), agent2.count(); got != want {
		t.Errorf("32 concurrent scrapes ran %d privileged commands, want %d (one collection)", got, want)
	}
}

// TestNilServiceSnapshotStaysSafe keeps the documented nil-safety, since
// the handler holds the service by pointer.
func TestNilServiceSnapshotStaysSafe(t *testing.T) {
	var svc *MetricsService
	snap := svc.Snapshot(context.Background())
	if snap.BuildVersion != "" {
		t.Error("a nil service produced data")
	}
}

// TestSnapshotWithoutAConstructorUsesTheDefaultTTL guards the zero-value
// path: a service built as a struct literal must still cache.
func TestSnapshotWithoutAConstructorUsesTheDefaultTTL(t *testing.T) {
	agent := &scrapeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	svc := &MetricsService{cfg: cfg, dns: NewDNSService(cfg)}

	svc.Snapshot(context.Background())
	first := agent.count()
	svc.Snapshot(context.Background())

	if got := agent.count(); got != first {
		t.Errorf("the zero-value service collected twice (%d then %d)", first, got)
	}
}

// TestExpositionStillRendersFromTheCache confirms a cached snapshot is
// still a usable one, so the caching did not break the endpoint.
func TestExpositionStillRendersFromTheCache(t *testing.T) {
	svc, _ := newScrapeTestMetrics(t)
	ctx := context.Background()

	svc.Snapshot(ctx)
	snap := svc.Snapshot(ctx)

	var sb strings.Builder
	if err := snap.Write(&sb); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(sb.String(), "# TYPE") {
		t.Errorf("the cached snapshot did not render exposition output:\n%s", sb.String())
	}
}
