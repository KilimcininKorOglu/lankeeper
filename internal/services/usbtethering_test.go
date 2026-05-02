package services_test

import (
	"context"
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
)

func TestNewUSBTetheringService(t *testing.T) {
	cfg := &config.Config{}
	cfg.USBTether.Enabled = true
	cfg.USBTether.Interface = "usb0"
	cfg.USBTether.Metric = 100

	svc := services.NewUSBTetheringService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestUSBTetheringStatusNoPhone(t *testing.T) {
	cfg := &config.Config{}
	cfg.USBTether.Interface = "usb0"

	svc := services.NewUSBTetheringService(cfg)
	status, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if status.PhoneConnected {
		t.Error("should not detect phone when usb0 doesn't exist")
	}
	if status.ActiveWAN {
		t.Error("should not be active WAN by default")
	}
}

func TestUSBTetheringIsActiveDefault(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewUSBTetheringService(cfg)

	if svc.IsActive() {
		t.Error("should not be active by default")
	}
}

func TestUSBTetheringIsPhoneConnectedFalse(t *testing.T) {
	cfg := &config.Config{}
	cfg.USBTether.Interface = "nonexistent999"

	svc := services.NewUSBTetheringService(cfg)
	if svc.IsPhoneConnected() {
		t.Error("should return false for nonexistent interface")
	}
}
