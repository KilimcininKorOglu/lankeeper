package services_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
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
	if _, err := exec.LookPath("ping"); err != nil {
		t.Skip("ping not available in this environment")
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

func TestHealthCheckResetCounter(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.HealthCheck.Checks = []config.HealthCheckEntry{
		{Name: "test", Interval: "1h"},
	}

	svc := services.NewHealthCheckService(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
