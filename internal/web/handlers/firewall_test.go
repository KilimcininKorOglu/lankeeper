package handlers_test

import (
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/web/handlers"
)

func TestNewFirewallHandler(t *testing.T) {
	cfg := &config.Config{}
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "eth0", Role: "wan"},
		{ID: "lan", Device: "eth1", Role: "lan"},
	}

	svc, err := services.NewFirewallServiceFromFS(cfg, "flush ruleset\n")
	if err != nil {
		t.Fatalf("new firewall service: %v", err)
	}

	h := handlers.NewFirewallHandler(nil, svc, cfg)
	if h == nil {
		t.Fatal("handler should not be nil")
	}
}
