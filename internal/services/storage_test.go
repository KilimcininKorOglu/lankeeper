package services_test

import (
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
)

func TestNewStorageService(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewStorageService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestNewBackupService(t *testing.T) {
	svc := services.NewBackupService("/etc/home-router")
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestNewSyslogService(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewSyslogService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestNewNTPService(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewNTPService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}
