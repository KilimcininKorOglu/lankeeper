package services_test

import (
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/services"
)

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.1.0", "v1.0.0", 1},
		{"v1.0.0", "v1.1.0", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.0.1", "v1.0.0", 1},
		{"v0.9.0", "v1.0.0", -1},
		{"v1.2.3", "v1.2.3", 0},
		{"1.0.0", "v1.0.0", 0},
		{"v1.0.0-rc1", "v1.0.0", 0},
		{"dev", "v1.0.0", -1},
	}

	for _, tt := range tests {
		got := services.CompareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestNewUpdateService(t *testing.T) {
	svc := services.NewUpdateService("v1.0.0", "abc1234", "2026-01-01", nil)
	if svc == nil {
		t.Fatal("service should not be nil")
	}

	info := svc.GetVersionInfo()
	if info.Version != "v1.0.0" {
		t.Errorf("version = %q, want v1.0.0", info.Version)
	}
	if info.Commit != "abc1234" {
		t.Errorf("commit = %q, want abc1234", info.Commit)
	}
}

func TestUpdateServiceNoPending(t *testing.T) {
	svc := services.NewUpdateService("v1.0.0", "abc", "2026-01-01", nil)
	if svc.HasPendingUpdate() {
		t.Error("should not have pending update initially")
	}
	if svc.PendingVersion() != "" {
		t.Errorf("pending version = %q, want empty", svc.PendingVersion())
	}
}
