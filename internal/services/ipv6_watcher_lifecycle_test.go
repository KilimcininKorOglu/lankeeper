package services_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// newWatchedService returns a service whose lease dispatches are
// observable, with the state file pointed at a temp dir.
func newWatchedService(t *testing.T) (*services.IPv6Service, chan struct{}, string) {
	t.Helper()
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)
	statePath := filepath.Join(t.TempDir(), "ipv6-prefix.json")
	svc.SetStatePathForTest(statePath)

	dispatched := make(chan struct{}, 8)
	svc.SetOnLeaseChange(func(context.Context, services.PrefixState) error {
		select {
		case dispatched <- struct{}{}:
		default:
		}
		return nil
	})
	return svc, dispatched, statePath
}

// writeLease drops a fresh lease in place the way the dhcp6c hook does,
// with a distinct prefix so the watcher's hash-based dedupe does not
// swallow it.
func writeLease(t *testing.T, statePath, prefix string) {
	t.Helper()
	body := fmt.Sprintf(
		`{"timestamp":%d,"reason":"REPLY","prefix":%q,"prefixLength":56,"preferredLifetime":3600,"validLifetime":7200}`,
		time.Now().Unix(), prefix)
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatalf("write lease: %v", err)
	}
	if err := os.Rename(tmp, statePath); err != nil {
		t.Fatalf("install lease: %v", err)
	}
}

// TestLeaseWatcherStopsWhenTheContextIsCancelled is half the regression.
// The watcher was launched with context.Background(), so its ctx.Done
// branch could never fire and the only exit left was StopLeaseWatcher,
// which had no production caller at all. The goroutine and its debounce
// timer ran unmanaged for the life of the process.
func TestLeaseWatcherStopsWhenTheContextIsCancelled(t *testing.T) {
	svc, dispatched, _ := newWatchedService(t)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if err := svc.StartLeaseWatcher(ctx, &wg); err != nil {
		t.Fatalf("StartLeaseWatcher: %v", err)
	}
	t.Cleanup(svc.StopLeaseWatcher)

	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher never made its initial dispatch")
	}

	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling the context did not stop the watcher")
	}
}

// TestLeaseWatcherJoinsTheShutdownDrain is the other half. Exiting on
// cancel is not enough on its own: the server drains its background
// goroutines before returning, and a watcher that is not counted lets
// shutdown proceed while a dispatch is still running. A dispatch applies
// the firewall ruleset and auto-confirms the watchdog, so abandoning one
// mid-step is exactly what the drain exists to prevent.
func TestLeaseWatcherJoinsTheShutdownDrain(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)
	svc.SetStatePathForTest(filepath.Join(t.TempDir(), "ipv6-prefix.json"))

	var finished atomic.Bool
	entered := make(chan struct{}, 1)
	svc.SetOnLeaseChange(func(context.Context, services.PrefixState) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		// Long enough that an uncounted goroutine is demonstrably still
		// working when an unwired Wait returns.
		time.Sleep(300 * time.Millisecond)
		finished.Store(true)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if err := svc.StartLeaseWatcher(ctx, &wg); err != nil {
		t.Fatalf("StartLeaseWatcher: %v", err)
	}
	t.Cleanup(svc.StopLeaseWatcher)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the dispatch never started")
	}
	cancel()
	wg.Wait()

	if !finished.Load() {
		t.Error("the drain returned while a lease dispatch was still running")
	}
}

// TestLeaseWatcherCanRestartAfterACancel covers the state the exit path
// leaves behind. The running marker was only cleared by StopLeaseWatcher,
// so a watcher that returned on its own left it set and every later
// start silently reported success without starting anything.
func TestLeaseWatcherCanRestartAfterACancel(t *testing.T) {
	svc, dispatched, statePath := newWatchedService(t)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if err := svc.StartLeaseWatcher(ctx, &wg); err != nil {
		t.Fatalf("first StartLeaseWatcher: %v", err)
	}
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("the first watcher never dispatched")
	}
	cancel()
	wg.Wait()

	// A lease arrived while nothing was watching, so the restarted
	// watcher has a genuinely new state to report on its initial pass.
	writeLease(t, statePath, "2001:db8:1::")

	restarted := t.Context()
	if err := svc.StartLeaseWatcher(restarted, nil); err != nil {
		t.Fatalf("second StartLeaseWatcher: %v", err)
	}
	t.Cleanup(svc.StopLeaseWatcher)

	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("the restarted watcher never dispatched, so the first one never released the slot")
	}
}

// TestLeaseWatcherStaysIdempotentWhileRunning keeps the documented
// no-op behaviour: a second start must not install a second watcher.
func TestLeaseWatcherStaysIdempotentWhileRunning(t *testing.T) {
	svc, dispatched, _ := newWatchedService(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	if err := svc.StartLeaseWatcher(ctx, &wg); err != nil {
		t.Fatalf("StartLeaseWatcher: %v", err)
	}
	t.Cleanup(svc.StopLeaseWatcher)

	<-dispatched
	if err := svc.StartLeaseWatcher(ctx, &wg); err != nil {
		t.Fatalf("second StartLeaseWatcher: %v", err)
	}

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a second goroutine was started and never drained")
	}
}
