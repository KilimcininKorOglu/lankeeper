package services

import (
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestHealthCheckCooldownIsNotEndedByAnEarlierOne is the regression
// test. Each cooldown started a goroutine that slept and then cleared
// the flag. After an operator reset, the next action's cooldown was
// cleared when the earlier goroutine woke, so a remediation such as an
// interface restart could run again long before its cooldown ended.
func TestHealthCheckCooldownIsNotEndedByAnEarlierOne(t *testing.T) {
	svc := NewHealthCheckService(&config.Config{})
	svc.results["wan"] = &CheckResult{Name: "wan"}

	svc.startCooldown("wan", 20*time.Millisecond)
	svc.ResetCounter("wan")
	svc.startCooldown("wan", time.Hour)

	time.Sleep(100 * time.Millisecond)
	if r := svc.GetResult("wan"); !r.InCooldown {
		t.Errorf("an earlier cooldown ended the current one: %+v", *r)
	}
}

// TestHealthCheckCooldownEnds pins that a cooldown ends on its own and
// that a reset ends it at once.
func TestHealthCheckCooldownEnds(t *testing.T) {
	svc := NewHealthCheckService(&config.Config{})
	svc.results["wan"] = &CheckResult{Name: "wan"}

	svc.startCooldown("wan", 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if svc.GetResult("wan").InCooldown {
		t.Error("the cooldown did not end")
	}

	svc.startCooldown("wan", time.Hour)
	svc.ResetCounter("wan")
	if svc.GetResult("wan").InCooldown {
		t.Error("a reset did not end the cooldown")
	}
}
