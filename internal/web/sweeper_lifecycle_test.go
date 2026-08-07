package web

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// sweeperGoroutines counts the cleanup goroutines currently parked in
// this package. Matching on the function name is what makes the leak
// observable: a total goroutine count would move with whatever else the
// test binary happens to be doing.
func sweeperGoroutines(t *testing.T) int {
	t.Helper()

	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	stacks := string(buf[:n])

	count := 0
	for _, frame := range []string{
		"web.(*RateLimiter).cleanup",
		"web.(*loginGuard).cleanup",
	} {
		count += strings.Count(stacks, frame)
	}
	return count
}

// waitForSweepers gives a stopped goroutine a moment to unwind. Stop only
// closes a channel; the goroutine still has to be scheduled to return.
func waitForSweepers(t *testing.T, want int) int {
	t.Helper()

	var got int
	for range 200 {
		got = sweeperGoroutines(t)
		if got == want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return got
}

// stableSweeperBaseline waits for the count to stop moving before a test
// measures against it.
//
// `go rl.cleanup()` returns before the goroutine is scheduled, and Stop
// only closes a channel, so a sweeper created or ended by an earlier
// test can appear or vanish inside the next one's measurement window.
// Sampling a moving number made these tests fail about one run in eight.
func stableSweeperBaseline(t *testing.T) int {
	t.Helper()

	last := sweeperGoroutines(t)
	for range 200 {
		time.Sleep(20 * time.Millisecond)
		got := sweeperGoroutines(t)
		if got == last {
			return got
		}
		last = got
	}
	t.Fatalf("the sweeper count never settled (last %d)", last)
	return last
}

// TestAStoppedLimiterReleasesItsGoroutine is the regression test. The
// cleanup loop was `for range ticker.C` with no cancellation input at
// all, so nothing short of process exit could end it. Three fixed
// instances that die with the server hid the cost; a test binary, or any
// future path that rebuilds a limiter on a config reload, does not.
//
// Counts process-wide goroutines, so no t.Parallel here.
func TestAStoppedLimiterReleasesItsGoroutine(t *testing.T) {
	before := stableSweeperBaseline(t)

	limiters := make([]*RateLimiter, 0, 20)
	for range 20 {
		limiters = append(limiters, NewRateLimiter(time.Hour, 1))
	}
	if got := waitForSweepers(t, before+20); got != before+20 {
		t.Fatalf("after constructing 20 limiters there are %d sweepers, want %d", got, before+20)
	}

	for _, rl := range limiters {
		rl.Stop()
	}
	if got := waitForSweepers(t, before); got != before {
		t.Errorf("after stopping every limiter there are %d sweepers, want %d", got, before)
	}
}

// TestStopIsIdempotent lets a caller stop without tracking whether
// shutdown already ran. Closing a closed channel panics, so this is the
// difference between a safe API and a crash on a double shutdown.
func TestStopIsIdempotent(t *testing.T) {
	rl := NewRateLimiter(time.Hour, 1)
	rl.Stop()
	rl.Stop()
	rl.Stop()

	g := newLoginGuard()
	g.Stop()
	g.Stop()
}

// TestAStoppedLimiterStillDecides keeps Stop scoped to the sweeper. The
// server stops its limiters while requests may still be in flight, so a
// stopped limiter must keep answering rather than start letting
// everything through.
func TestAStoppedLimiterStillDecides(t *testing.T) {
	rl := NewRateLimiter(time.Hour, 1)
	rl.Stop()

	const ip = "10.10.10.50"
	if !rl.Allow(ip) {
		t.Fatal("a stopped limiter refused the first request")
	}
	if rl.Allow(ip) {
		t.Error("a stopped limiter stopped enforcing the burst")
	}
}

// TestTheLoginGuardReleasesItsGoroutine covers the second sweeper, which
// had the same shape.
func TestTheLoginGuardReleasesItsGoroutine(t *testing.T) {
	before := stableSweeperBaseline(t)

	guards := make([]*loginGuard, 0, 10)
	for range 10 {
		guards = append(guards, newLoginGuard())
	}
	if got := waitForSweepers(t, before+10); got != before+10 {
		t.Fatalf("after constructing 10 guards there are %d sweepers, want %d", got, before+10)
	}

	for _, g := range guards {
		g.Stop()
	}
	if got := waitForSweepers(t, before); got != before {
		t.Errorf("after stopping every guard there are %d sweepers, want %d", got, before)
	}
}

// TestTheServerStopsEverySweeperItBuilt is the wiring check. Two of the
// three limiters used to be locals that nothing else held a reference
// to, so even with a Stop method the server could not have called it.
func TestTheServerStopsEverySweeperItBuilt(t *testing.T) {
	before := stableSweeperBaseline(t)

	srv := newLockoutTestServer(t, "correct-horse")
	if len(srv.limiters) != 3 {
		t.Errorf("the server registered %d limiters, want the 3 it constructs", len(srv.limiters))
	}
	if got := waitForSweepers(t, before+len(srv.limiters)+1); got != before+len(srv.limiters)+1 {
		t.Fatalf("the server started %d sweepers, want one per limiter plus the login guard",
			got-before)
	}

	srv.stopBackgroundSweepers()

	if got := waitForSweepers(t, before); got != before {
		t.Errorf("shutdown left %d sweepers running", got-before)
	}
}
