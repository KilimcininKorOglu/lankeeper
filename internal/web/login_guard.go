package web

import (
	"log"
	"sync"
	"time"
)

// The rate limiter in front of the login route caps how fast an address
// may attempt, but it treats a correct password and a wrong one alike,
// so a run of guesses costs an attacker only patience. These figures add
// a cost that grows with the run itself.
//
// The threshold matches the limiter's burst: an operator who mistypes
// enough times to exhaust the burst is exactly the one who reaches this,
// and five wrong passwords in a row is already past a plausible slip.
// From there each further failure doubles the wait, so a sustained run
// settles at five attempts per quarter hour instead of one every five
// seconds. A stretch of quiet clears the record, so a bad day today does
// not shorten the operator's patience next week.
const (
	loginFailureThreshold = 5
	loginLockoutBase      = time.Minute
	loginLockoutMax       = 15 * time.Minute
	loginFailureIdleReset = time.Hour
)

// loginGuard tracks consecutive authentication failures per client
// address and locks that address out for a growing interval.
//
// The key is the address rather than the account because there is one
// fixed admin and no username field. A single global counter would let
// any device on the segment lock the operator out of their own router,
// which trades a guessing risk for a denial-of-service one. The state is
// in memory only, so restarting the service clears every lockout; that
// is the operator's escape hatch if they lock themselves out and do not
// want to wait.
type loginGuard struct {
	mu      sync.Mutex
	clients map[string]*loginRecord

	// stop ends the sweeper, for the same reason the rate limiter has
	// one: a ticker loop with no cancellation input strands a goroutine
	// for the life of the process.
	stop     chan struct{}
	stopOnce sync.Once
}

type loginRecord struct {
	failures    int
	lockedUntil time.Time
	lastSeen    time.Time
}

func newLoginGuard() *loginGuard {
	g := &loginGuard{
		clients: make(map[string]*loginRecord),
		stop:    make(chan struct{}),
	}
	go g.cleanup()
	return g
}

// Stop ends the cleanup goroutine. Calling it more than once is safe.
func (g *loginGuard) Stop() {
	g.stopOnce.Do(func() { close(g.stop) })
}

// cleanup drops records that have gone quiet, so a long-lived appliance
// does not accumulate one entry per address that ever mistyped.
func (g *loginGuard) cleanup() {
	ticker := time.NewTicker(loginFailureIdleReset)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			g.mu.Lock()
			now := time.Now()
			for ip, rec := range g.clients {
				if now.Sub(rec.lastSeen) > loginFailureIdleReset && now.After(rec.lockedUntil) {
					delete(g.clients, ip)
				}
			}
			g.mu.Unlock()
		}
	}
}

// LockedFor reports how long the address must wait before its next
// attempt is considered. Zero means it may attempt now.
func (g *loginGuard) LockedFor(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()

	rec, ok := g.clients[ip]
	if !ok {
		return 0
	}

	now := time.Now()
	// A quiet stretch forgives the run, whether or not it ended in a
	// lockout. Checked on read as well as in cleanup, because the ticker
	// only fires while the process is up long enough to reach it.
	if now.Sub(rec.lastSeen) > loginFailureIdleReset {
		delete(g.clients, ip)
		return 0
	}
	if remaining := time.Until(rec.lockedUntil); remaining > 0 {
		return remaining
	}
	return 0
}

// RecordFailure counts a wrong password and returns the lockout it
// earned, zero if the address is still below the threshold.
func (g *loginGuard) RecordFailure(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	rec, ok := g.clients[ip]
	if !ok || now.Sub(rec.lastSeen) > loginFailureIdleReset {
		rec = &loginRecord{}
		g.clients[ip] = rec
	}
	rec.failures++
	rec.lastSeen = now

	if rec.failures < loginFailureThreshold {
		return 0
	}

	// Double per failure past the threshold, capped. Computed from the
	// running total rather than the previous lockout so the interval
	// cannot be reset by waiting one out.
	lockout := loginLockoutBase << (rec.failures - loginFailureThreshold)
	if lockout > loginLockoutMax || lockout <= 0 {
		lockout = loginLockoutMax
	}
	rec.lockedUntil = now.Add(lockout)
	return lockout
}

// RecordSuccess clears the run. Whoever just proved they hold the
// password is not the run this guard exists to slow down.
func (g *loginGuard) RecordSuccess(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.clients, ip)
}

// logAuthFailure marks the attempt in the appliance log.
//
// The request logger records a status, so a rejected login already
// leaves a 401 line, but that line is one of many and says nothing about
// how far a run has gone. An operator scanning for trouble needs the
// word and the count in one place.
func logAuthFailure(ip string, failures int, lockout time.Duration) {
	if lockout > 0 {
		// ip comes from net.SplitHostPort on RemoteAddr, which the
		// kernel supplies. No forwarded header is trusted here.
		// #nosec G706
		log.Printf("auth: failed login from %s (%d consecutive), locked out for %s", ip, failures, lockout)
		return
	}
	// Same kernel-supplied address as above.
	// #nosec G706
	log.Printf("auth: failed login from %s (%d consecutive)", ip, failures)
}

// failureCount reports the running total for an address, for the log
// line and for tests.
func (g *loginGuard) failureCount(ip string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if rec, ok := g.clients[ip]; ok {
		return rec.failures
	}
	return 0
}
