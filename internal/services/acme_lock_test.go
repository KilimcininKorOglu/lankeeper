package services

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

func newLockTestServices(t *testing.T) (*TLSService, *ACMEService, *config.Config) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.SetFilePath(t.TempDir() + "/router.yaml")
	dir := t.TempDir()
	tlsSvc := NewTLSServiceInDir(cfg, dir)
	return tlsSvc, NewACMEServiceInDir(cfg, tlsSvc, dir, "https://acme.invalid/directory", "https://cf.invalid"), cfg
}

// A renewal runs for minutes against the CA. If the operator switched to
// another mode meanwhile, installing the renewed pair would overwrite
// their certificate and put the mode back to acme.
func TestRenewalDoesNotInstallAfterAModeSwitch(t *testing.T) {
	_, acmeSvc, cfg := newLockTestServices(t)
	cfg.System.TLS.Mode = "self-signed"
	cfg.System.TLS.ACME.Enabled = true

	if _, err := acmeSvc.installACMEPair(nil, nil, true); !errors.Is(err, ErrACMEModeChanged) {
		t.Fatalf("install after a mode switch: err = %v, want ErrACMEModeChanged", err)
	}
	if cfg.System.TLS.Mode != "self-signed" {
		t.Errorf("mode became %q", cfg.System.TLS.Mode)
	}
}

// The settings handlers and the renewal goroutine share cfg.System.TLS;
// run under -race, this fails when any of them skips the lock.
func TestTLSSettingsAccessIsSerialized(t *testing.T) {
	tlsSvc, acmeSvc, _ := newLockTestServices(t)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 5 {
			if _, err := tlsSvc.SwitchMode(context.Background(), "self-signed", "hermes.lan", nil, 30); err != nil {
				t.Errorf("switch mode: %v", err)
			}
		}
	})
	wg.Go(func() {
		for range 50 {
			next := acmeSvc.tlsSettings().ACME
			next.Domain = "hermes.example"
			if _, err := acmeSvc.SetSettings(next, false); err != nil {
				t.Errorf("set settings: %v", err)
			}
			_ = tlsSvc.Settings()
		}
	})
	wg.Wait()
}
