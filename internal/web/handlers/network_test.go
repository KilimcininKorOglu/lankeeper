package handlers_test

import (
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/web/handlers"
)

func TestNewNetworkHandler(t *testing.T) {
	cfg := &config.Config{}

	network := services.NewNetworkService(cfg)
	pppoe := services.NewPPPoEService(cfg)
	usb := services.NewUSBTetheringService(cfg)
	health := services.NewHealthCheckService(cfg)

	h := handlers.NewNetworkHandler(nil, network, pppoe, usb, health)
	if h == nil {
		t.Fatal("handler should not be nil")
	}
}
