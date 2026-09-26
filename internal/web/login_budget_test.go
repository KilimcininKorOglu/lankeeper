package web

import (
	"testing"
	"time"
)

// One address at a time stays under its own guard, so only an aggregate
// count sees a run spread across the subnet. Past the budget, checks are
// spaced out globally and a queue longer than the maximum wait is
// refused, while nothing locks the operator out for good.
func TestLoginBudgetSpacesChecksOnceSpent(t *testing.T) {
	var b loginBudget
	now := time.Unix(1_000_000, 0)

	for range loginBudgetFailures {
		expectReservation(t, &b, now, 0, true)
		b.RecordFailure(now)
	}

	expectReservation(t, &b, now, 0, true)
	expectReservation(t, &b, now, loginBudgetSpacing, true)
	if _, ok := b.Reserve(now); ok {
		t.Fatal("a slot beyond the maximum wait was granted")
	}

	// Once the window passes without failures, attempts run at once.
	expectReservation(t, &b, now.Add(loginBudgetWindow+time.Second), 0, true)
}

func expectReservation(t *testing.T, b *loginBudget, now time.Time, wantWait time.Duration, wantOK bool) {
	t.Helper()
	if wait, ok := b.Reserve(now); wait != wantWait || ok != wantOK {
		t.Fatalf("Reserve = %s, %v; want %s, %v", wait, ok, wantWait, wantOK)
	}
}
