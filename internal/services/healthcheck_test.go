package services_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func TestNewHealthCheckService(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewHealthCheckService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestHealthCheckGetResultsEmpty(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewHealthCheckService(cfg)

	results := svc.GetResults()
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestHealthCheckGetResultNotFound(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewHealthCheckService(cfg)

	result := svc.GetResult("nonexistent")
	if result != nil {
		t.Error("should return nil for nonexistent check")
	}
}

func TestHealthCheckStartStop(t *testing.T) {
	pingPath, err := exec.LookPath("ping")
	if err != nil {
		t.Skip("ping binary not found")
	}
	if out, err := exec.Command(pingPath, "-c", "1", "-W", "3", "127.0.0.1").CombinedOutput(); err != nil {
		t.Skipf("ping 127.0.0.1 not functional in this environment: %s", out)
	}

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.HealthCheck.Enabled = true
	cfg.HealthCheck.Checks = []config.HealthCheckEntry{
		{
			Name:             "test-check",
			Interface:        "lo",
			Interval:         "1s",
			Timeout:          "3s",
			FailureThreshold: 3,
			Cooldown:         "1s",
			Targets: []config.HealthCheckTarget{
				{Type: "ping", Host: "127.0.0.1"},
			},
		},
	}

	svc := services.NewHealthCheckService(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)

	time.Sleep(2 * time.Second)

	result := svc.GetResult("test-check")
	if result == nil {
		t.Fatal("should have result after starting")
	}
	if result.Status != "ok" {
		t.Errorf("pinging localhost should succeed, got status=%q", result.Status)
	}

	cancel()
	svc.Stop()
}

// TestHealthCheckEnabledGatesStart pins the meaning of the enabled
// flag. Before the flag was honoured, nothing in production read it, so
// toggling it had no observable effect in either direction.
//
// Both halves use a 1h interval so no probe ever fires: the assertion
// is on the result map Start seeds synchronously, which makes the test
// deterministic and independent of whether ping works in the sandbox.
func TestHealthCheckEnabledGatesStart(t *testing.T) {
	newCfg := func(enabled bool) *config.Config {
		cfg := &config.Config{}
		cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
		cfg.HealthCheck.Enabled = enabled
		cfg.HealthCheck.Checks = []config.HealthCheckEntry{
			{
				Name:     "wan-internet",
				Interval: "1h",
				Targets: []config.HealthCheckTarget{
					{Type: "ping", Host: "127.0.0.1"},
				},
			},
		}
		return cfg
	}

	t.Run("disabled", func(t *testing.T) {
		svc := services.NewHealthCheckService(newCfg(false))

		ctx := t.Context()
		svc.Start(ctx)

		if results := svc.GetResults(); len(results) != 0 {
			t.Errorf("disabled service seeded %d results, want 0", len(results))
		}
		if svc.GetResult("wan-internet") != nil {
			t.Error("disabled service produced a result for the configured check")
		}
	})

	t.Run("enabled", func(t *testing.T) {
		svc := services.NewHealthCheckService(newCfg(true))

		ctx := t.Context()
		svc.Start(ctx)

		result := svc.GetResult("wan-internet")
		if result == nil {
			t.Fatal("enabled service did not seed the configured check")
		}
		if result.Status != "unknown" {
			t.Errorf("freshly seeded status = %q, want %q", result.Status, "unknown")
		}
	})
}

func TestHealthCheckResetCounter(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.HealthCheck.Enabled = true
	cfg.HealthCheck.Checks = []config.HealthCheckEntry{
		{Name: "test", Interval: "1h"},
	}

	svc := services.NewHealthCheckService(cfg)

	ctx := t.Context()
	svc.Start(ctx)

	svc.ResetCounter("test")
	result := svc.GetResult("test")
	if result == nil {
		t.Fatal("should have result")
	}
	if result.FailureCount != 0 {
		t.Error("failure count should be 0 after reset")
	}
	if result.Status != "ok" {
		t.Errorf("status should be ok after reset, got %q", result.Status)
	}
}
