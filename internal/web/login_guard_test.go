package web

import (
	"testing"
	"time"
)

// TestARunOfFailuresEarnsALockout is the regression test. The login
// route had a rate limiter and nothing else: every attempt was paced the
// same whether the password was right or wrong, so a guessing run cost
// an attacker only patience. Nothing grew more expensive as the run grew
// longer.
func TestARunOfFailuresEarnsALockout(t *testing.T) {
	g := &loginGuard{clients: make(map[string]*loginRecord)}
	const ip = "10.10.10.20"

	for i := 1; i < loginFailureThreshold; i++ {
		if lockout := g.RecordFailure(ip); lockout != 0 {
			t.Fatalf("failure %d locked the address out early: %s", i, lockout)
		}
		if wait := g.LockedFor(ip); wait != 0 {
			t.Fatalf("failure %d left a wait of %s", i, wait)
		}
	}

	lockout := g.RecordFailure(ip)
	if lockout != loginLockoutBase {
		t.Fatalf("lockout at the threshold = %s, want %s", lockout, loginLockoutBase)
	}
	if wait := g.LockedFor(ip); wait <= 0 || wait > loginLockoutBase {
		t.Errorf("LockedFor = %s, want a positive value no greater than %s", wait, loginLockoutBase)
	}
}

// TestTheLockoutDoublesAndIsCapped covers the progressive half. Waiting
// one lockout out must not buy back the cheap attempts, or the control
// reduces to a fixed delay an attacker simply sleeps through.
func TestTheLockoutDoublesAndIsCapped(t *testing.T) {
	g := &loginGuard{clients: make(map[string]*loginRecord)}
	const ip = "10.10.10.21"

	for i := 1; i < loginFailureThreshold; i++ {
		g.RecordFailure(ip)
	}

	want := loginLockoutBase
	var capped bool
	for i := 0; i < 10; i++ {
		got := g.RecordFailure(ip)
		if want > loginLockoutMax {
			want = loginLockoutMax
		}
		if got != want {
			t.Fatalf("lockout %d = %s, want %s", i, got, want)
		}
		if got == loginLockoutMax {
			capped = true
		}
		want *= 2
	}
	if !capped {
		t.Error("the lockout never reached its cap")
	}
}

// TestTheCapHoldsAgainstAVeryLongRun guards the shift arithmetic: a
// large failure count shifts the base past the width of the type, which
// wraps to zero or negative and would hand an attacker no lockout at all
// precisely when the run is longest.
func TestTheCapHoldsAgainstAVeryLongRun(t *testing.T) {
	g := &loginGuard{clients: make(map[string]*loginRecord)}
	const ip = "10.10.10.22"

	g.mu.Lock()
	g.clients[ip] = &loginRecord{failures: 200, lastSeen: time.Now()}
	g.mu.Unlock()

	if got := g.RecordFailure(ip); got != loginLockoutMax {
		t.Errorf("lockout after 200 failures = %s, want the cap %s", got, loginLockoutMax)
	}
}

// TestSuccessClearsTheRun keeps the operator from paying for an
// attacker's attempts, and keeps a mistyped password from following them
// around after they get in.
func TestSuccessClearsTheRun(t *testing.T) {
	g := &loginGuard{clients: make(map[string]*loginRecord)}
	const ip = "10.10.10.23"

	for i := 0; i < loginFailureThreshold-1; i++ {
		g.RecordFailure(ip)
	}
	g.RecordSuccess(ip)

	if got := g.failureCount(ip); got != 0 {
		t.Errorf("failure count after a success = %d, want 0", got)
	}
	if lockout := g.RecordFailure(ip); lockout != 0 {
		t.Errorf("the next failure locked out immediately: %s", lockout)
	}
}

// TestOneAddressCannotLockOutAnother is why the guard keys on the
// address rather than the account. There is one fixed admin and no
// username, so a single global counter would let any device on the
// segment lock the operator out of their own router, trading a guessing
// risk for a denial-of-service one.
func TestOneAddressCannotLockOutAnother(t *testing.T) {
	g := &loginGuard{clients: make(map[string]*loginRecord)}
	const attacker = "10.10.10.24"
	const operator = "10.10.10.25"

	for i := 0; i < loginFailureThreshold*3; i++ {
		g.RecordFailure(attacker)
	}
	if g.LockedFor(attacker) == 0 {
		t.Fatal("the attacking address was not locked out")
	}
	if wait := g.LockedFor(operator); wait != 0 {
		t.Errorf("the operator's address inherited a %s lockout", wait)
	}
}

// TestAQuietStretchForgivesTheRun keeps a bad day from shortening the
// operator's patience next week. The check has to happen on read too,
// since the cleanup ticker only fires while the process stays up.
func TestAQuietStretchForgivesTheRun(t *testing.T) {
	g := &loginGuard{clients: make(map[string]*loginRecord)}
	const ip = "10.10.10.26"

	stale := time.Now().Add(-loginFailureIdleReset - time.Minute)
	g.mu.Lock()
	g.clients[ip] = &loginRecord{failures: loginFailureThreshold * 2, lastSeen: stale}
	g.mu.Unlock()

	if wait := g.LockedFor(ip); wait != 0 {
		t.Errorf("a run last seen %s ago still held a %s lockout", loginFailureIdleReset, wait)
	}
	if lockout := g.RecordFailure(ip); lockout != 0 {
		t.Errorf("the first failure after a quiet stretch locked out: %s", lockout)
	}
	if got := g.failureCount(ip); got != 1 {
		t.Errorf("failure count = %d, want the run to have restarted at 1", got)
	}
}

// TestAnExpiredLockoutStillCountsTheRun is the difference between a
// backoff and a fixed delay: the attempts that follow an expired lockout
// must earn a longer one, not the base again.
func TestAnExpiredLockoutStillCountsTheRun(t *testing.T) {
	g := &loginGuard{clients: make(map[string]*loginRecord)}
	const ip = "10.10.10.27"

	for i := 0; i < loginFailureThreshold; i++ {
		g.RecordFailure(ip)
	}
	// Expire the lockout without touching the count, as the clock would.
	g.mu.Lock()
	g.clients[ip].lockedUntil = time.Now().Add(-time.Second)
	g.mu.Unlock()

	if wait := g.LockedFor(ip); wait != 0 {
		t.Fatalf("an expired lockout still reported %s", wait)
	}
	if got := g.RecordFailure(ip); got != loginLockoutBase*2 {
		t.Errorf("the next failure earned %s, want %s", got, loginLockoutBase*2)
	}
}
