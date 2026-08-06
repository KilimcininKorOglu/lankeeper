package netutil

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

type AtomicChange struct {
	Service  string
	mu       sync.Mutex
	snapshot string
	applied  bool
	timer    *time.Timer
}

func NewAtomicChange(service string) *AtomicChange {
	return &AtomicChange{Service: service}
}

// NewAtomicChangeWithSnapshot rebuilds a change whose snapshot was taken
// by an earlier process. The watchdog state is persisted so an
// unconfirmed change still rolls back after a restart, and the snapshot
// field is unexported, so restoring needs this entry point.
//
// The change is marked applied because it only reaches persistence after
// Apply has succeeded.
func NewAtomicChangeWithSnapshot(service, snapshot string) *AtomicChange {
	return &AtomicChange{
		Service:  service,
		snapshot: snapshot,
		applied:  true,
	}
}

func (ac *AtomicChange) Snapshot(ctx context.Context) error {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	switch ac.Service {
	case "firewall":
		out, err := RunSimple(ctx, "nft", "list", "ruleset")
		if err != nil {
			return fmt.Errorf("snapshot firewall: %w", err)
		}
		ac.snapshot = out
	default:
		return fmt.Errorf("unknown service: %s", ac.Service)
	}

	return nil
}

func (ac *AtomicChange) Validate(ctx context.Context, configPath string) error {
	switch ac.Service {
	case "firewall":
		_, err := Run(ctx, "nft", "-c", "-f", configPath)
		if err != nil {
			return fmt.Errorf("validate firewall: %w", err)
		}
	default:
		return fmt.Errorf("unknown service: %s", ac.Service)
	}
	return nil
}

func (ac *AtomicChange) Apply(ctx context.Context, configPath string) error {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	switch ac.Service {
	case "firewall":
		_, err := Run(ctx, "nft", "-f", configPath)
		if err != nil {
			return fmt.Errorf("apply firewall: %w", err)
		}
	default:
		return fmt.Errorf("unknown service: %s", ac.Service)
	}

	ac.applied = true
	return nil
}

func (ac *AtomicChange) StartWatchdog(timeout time.Duration, rollbackFn func() error) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	ac.timer = time.AfterFunc(timeout, func() {
		log.Printf("watchdog timeout for %s — rolling back", ac.Service)
		if err := rollbackFn(); err != nil {
			log.Printf("rollback failed for %s: %v", ac.Service, err)
		}
	})
}

func (ac *AtomicChange) Confirm() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.timer != nil {
		ac.timer.Stop()
		ac.timer = nil
	}
	log.Printf("change confirmed for %s", ac.Service)
}

func (ac *AtomicChange) Rollback(ctx context.Context) error {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.timer != nil {
		ac.timer.Stop()
		ac.timer = nil
	}

	if ac.snapshot == "" {
		return fmt.Errorf("no snapshot to rollback to")
	}

	switch ac.Service {
	case "firewall":
		_, err := Run(ctx, "nft", "flush", "ruleset")
		if err != nil {
			return fmt.Errorf("flush for rollback: %w", err)
		}

		tmpFile := fmt.Sprintf("/tmp/nft-rollback-%d.conf", time.Now().UnixNano())
		if err := writeFile(tmpFile, []byte(ac.snapshot)); err != nil {
			return fmt.Errorf("write rollback: %w", err)
		}

		_, err = Run(ctx, "nft", "-f", tmpFile)
		if err != nil {
			return fmt.Errorf("apply rollback: %w", err)
		}
	}

	ac.applied = false
	log.Printf("rollback completed for %s", ac.Service)
	return nil
}

func (ac *AtomicChange) GetSnapshot() string {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.snapshot
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
