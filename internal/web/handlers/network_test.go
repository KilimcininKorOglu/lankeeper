package handlers_test

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/web/handlers"
)

func TestNewNetworkHandler(t *testing.T) {
	cfg := &config.Config{}

	network := services.NewNetworkService(cfg)
	pppoe := services.NewPPPoEService(cfg)
	usb := services.NewUSBTetheringService(cfg)
	health := services.NewHealthCheckService(cfg)
	firstBoot := services.NewFirstBootService(cfg)

	h := handlers.NewNetworkHandler(nil, network, pppoe, usb, health, firstBoot)
	if h == nil {
		t.Fatal("handler should not be nil")
	}
}
