package web_test

import (
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/web"
)

// TestSustainedRateMatchesTheRefillInterval is the regression test. The
// constructor took two plain integers, and the first was consumed as a
// per-second refill while the signature read like a per-window request
// count. NewRateLimiter(30, 60) therefore permitted thirty requests a
// second sustained, not thirty a minute, and the login limiter written
// as (1, 5) permitted one attempt a second rather than one every five.
//
// Draining the burst first isolates the refill: after that, exactly one
// request is earned back per interval and no more.
func TestSustainedRateMatchesTheRefillInterval(t *testing.T) {
	const refill = 50 * time.Millisecond
	rl := web.NewRateLimiter(refill, 2)
	t.Cleanup(rl.Stop)
	const ip = "10.10.10.7"

	for i := range 2 {
		if !rl.Allow(ip) {
			t.Fatalf("burst request %d was denied", i)
		}
	}
	if rl.Allow(ip) {
		t.Fatal("a request past the burst was allowed")
	}

	// One interval buys exactly one request back.
	time.Sleep(refill + 10*time.Millisecond)
	if !rl.Allow(ip) {
		t.Error("no token was earned back after a full interval")
	}
	if rl.Allow(ip) {
		t.Error("a full interval bought more than one request")
	}
}

// TestAnIntervalShorterThanTheFloorPanics guards the trap the Duration
// parameter does not close on its own: an untyped constant converts
// silently, so the old spelling NewRateLimiter(30, 60) still compiles
// and would mean thirty nanoseconds, a throttle that never throttles.
func TestAnIntervalShorterThanTheFloorPanics(t *testing.T) {
	for name, refill := range map[string]time.Duration{
		"the old integer spelling": 30,
		"zero":                     0,
		"negative":                 -time.Second,
		"just under the floor":     time.Millisecond - 1,
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewRateLimiter(%v, 60) was accepted", refill)
				}
			}()
			web.NewRateLimiter(refill, 60)
		})
	}
}

// TestTheFloorItselfIsAccepted keeps the guard from being off by one.
func TestTheFloorItselfIsAccepted(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("a 1ms interval panicked: %v", r)
		}
	}()
	web.NewRateLimiter(time.Millisecond, 1)
}

// TestBurstIsSpendableAtOnce pins the other half of the contract, since
// the two arguments are easy to transpose once both are meaningful.
func TestBurstIsSpendableAtOnce(t *testing.T) {
	rl := web.NewRateLimiter(time.Hour, 60)
	t.Cleanup(rl.Stop)
	const ip = "10.10.10.8"

	for i := range 60 {
		if !rl.Allow(ip) {
			t.Fatalf("request %d was denied inside a burst of 60", i)
		}
	}
	if rl.Allow(ip) {
		t.Error("a 61st request was allowed with an hour-long refill")
	}
}

// TestTheConfiguredBudgetsServeABrowsingOperator checks the numbers the
// server actually wires, using the same arithmetic. The global limiter
// counts static assets, so a cold page load costs seven requests; a
// limiter that cannot absorb several of those in a row locks the
// operator out of the appliance it protects.
func TestTheConfiguredBudgetsServeABrowsingOperator(t *testing.T) {
	const perPage = 7 // one HTML document plus six CSS and JS files
	rl := web.NewRateLimiter(200*time.Millisecond, 60)
	t.Cleanup(rl.Stop)
	const ip = "10.10.10.9"

	for page := range 8 {
		for i := range perPage {
			if !rl.Allow(ip) {
				t.Fatalf("request %d of page %d was throttled; "+
					"the operator would see a 429 while browsing", i, page)
			}
		}
	}
}

// TestTheLoginBudgetAbsorbsAMistypedPassword covers the other configured
// limiter from the same angle: it must be invisible to a person and
// expensive to an unattended guesser.
func TestTheLoginBudgetAbsorbsAMistypedPassword(t *testing.T) {
	rl := web.NewRateLimiter(5*time.Second, 5)
	t.Cleanup(rl.Stop)
	const ip = "10.10.10.10"

	for i := range 5 {
		if !rl.Allow(ip) {
			t.Fatalf("attempt %d was throttled; a mistyped password would lock the operator out", i)
		}
	}
	if rl.Allow(ip) {
		t.Error("a sixth immediate attempt was allowed")
	}
}
