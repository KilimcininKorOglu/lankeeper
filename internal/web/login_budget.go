package web

import (
	"log"
	"sync"
	"time"
)

// The per-address guard keys on the client address, and LANOnly admits
// every address of every LAN subnet, so a host that takes free addresses
// gets a fresh guard record and limiter bucket for each. These figures
// bound the total instead. They are far above what one person mistyping
// produces, and past them every further password check across all
// addresses waits for its own slot.
//
// Nothing is locked out: an operator who arrives during a run waits at
// most loginBudgetMaxWait, or is asked to retry, and gets in once the
// run stops. A global lockout would let any device on the segment keep
// the operator out of their own router.
const (
	loginBudgetFailures = 30
	loginBudgetWindow   = time.Hour
	loginBudgetSpacing  = 30 * time.Second
	loginBudgetMaxWait  = 30 * time.Second
)

// loginBudget counts failed logins across every address.
type loginBudget struct {
	mu       sync.Mutex
	failures []time.Time
	next     time.Time
	warned   time.Time
}

// prune drops failures older than the window. Caller must hold b.mu.
func (b *loginBudget) prune(now time.Time) {
	cut := 0
	for cut < len(b.failures) && now.Sub(b.failures[cut]) > loginBudgetWindow {
		cut++
	}
	b.failures = b.failures[cut:]
}

// Reserve returns how long this attempt has to wait before its password
// is checked. ok is false when the queue of reserved slots is longer
// than loginBudgetMaxWait, and the caller refuses the attempt; wait is
// then the time until a slot would be free.
func (b *loginBudget) Reserve(now time.Time) (wait time.Duration, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(now)
	if len(b.failures) < loginBudgetFailures {
		return 0, true
	}
	start := now
	if b.next.After(now) {
		start = b.next
	}
	wait = start.Sub(now)
	if wait > loginBudgetMaxWait {
		return wait, false
	}
	b.next = start.Add(loginBudgetSpacing)
	return wait, true
}

// RecordFailure counts a wrong password and logs, once per window, that
// the aggregate budget is spent: the per-address log lines alone show
// no single address doing anything unusual.
func (b *loginBudget) RecordFailure(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(now)
	b.failures = append(b.failures, now)
	if len(b.failures) >= loginBudgetFailures && now.Sub(b.warned) > loginBudgetWindow {
		b.warned = now
		log.Printf("auth: %d failed logins across all addresses in the last %s, spacing password checks %s apart",
			len(b.failures), loginBudgetWindow, loginBudgetSpacing)
	}
}
