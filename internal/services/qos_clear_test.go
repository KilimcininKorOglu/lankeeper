package services_test

import (
	"context"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestQoSClearRemovesTheIngressRedirect is the regression test. Cake adds
// an ingress qdisc on the WAN with a redirect to ifb0; clearing removed
// ifb0 but left the redirect, so every inbound WAN packet went to a
// device that no longer existed.
func TestQoSClearRemovesTheIngressRedirect(t *testing.T) {
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	cfg.Interfaces = []config.InterfaceConfig{{ID: "wan", Device: "eth0", Role: "wan"}}
	if err := services.NewQoSService(cfg).Clear(context.Background()); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if agent.countExec("tc", "qdisc", "del", "dev", "eth0", "ingress") != 1 {
		t.Error("clear left the WAN ingress qdisc and its redirect to ifb0 in place")
	}
}
