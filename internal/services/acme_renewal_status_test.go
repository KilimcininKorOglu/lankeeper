package services

import (
	"errors"
	"fmt"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// A renewal that cannot finish unattended must leave its TXT record and
// any failure where the settings page shows them, not only in the log.
func TestRenewalOutcomeIsRecorded(t *testing.T) {
	svc := NewACMEServiceInDir(config.DefaultConfig(), nil, t.TempDir(), "https://acme.invalid/directory", "https://cf.invalid")

	record := ManualRecord{Name: "_acme-challenge.hermes.example", Value: "token"}
	svc.recordRenewal(fmt.Errorf("issue: %w", &ManualChallengeError{Record: record}))
	got := svc.RenewalStatus()
	if got.Pending == nil || *got.Pending != record || got.Error != "" {
		t.Errorf("manual challenge recorded as %+v", got)
	}

	svc.recordRenewal(errors.New("dial tcp: no route to host"))
	if got := svc.RenewalStatus(); got.Pending != nil || got.Error != "dial tcp: no route to host" || got.At.IsZero() {
		t.Errorf("failure recorded as %+v", got)
	}

	svc.recordRenewal(nil)
	if got := svc.RenewalStatus(); got.Pending != nil || got.Error != "" {
		t.Errorf("success recorded as %+v", got)
	}
}
