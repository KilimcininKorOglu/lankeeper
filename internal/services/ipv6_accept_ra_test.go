package services

import (
	"context"
	"slices"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

func acceptRAConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp1s0", Role: "wan"},
		{ID: "lan", Device: "enp2s0", Role: "lan"},
	}
	cfg.IPv6.Enabled = "auto"
	cfg.IPv6.Mode = "dhcpv6-pd"
	cfg.IPv6.WAN.AcceptRA = true
	return cfg
}

// With forwarding on, the WAN learns its default route from the ISP's RA
// only at accept_ra=2, so acceptRA has to reach the kernel.
func TestAcceptRAReachesTheWANInterface(t *testing.T) {
	agent := &execLogAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	NewIPv6Service(acceptRAConfig()).ApplyAcceptRA(context.Background())
	if !slices.Contains(agent.lines, "sysctl -w net/ipv6/conf/enp1s0/accept_ra=2") {
		t.Errorf("accept_ra not set on the WAN; commands: %v", agent.lines)
	}
}

// pppd recreates ppp0 on every reconnect, so the default has to carry it.
func TestAcceptRACoversARecreatedPPPInterface(t *testing.T) {
	agent := &execLogAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := acceptRAConfig()
	cfg.PPPoE.Username = "subscriber"
	NewIPv6Service(cfg).ApplyAcceptRA(context.Background())
	for _, want := range []string{"sysctl -w net/ipv6/conf/ppp0/accept_ra=2", "sysctl -w net/ipv6/conf/default/accept_ra=2"} {
		if !slices.Contains(agent.lines, want) {
			t.Errorf("missing %q; commands: %v", want, agent.lines)
		}
	}
}

func TestAcceptRAIsLeftAloneWhenDisabled(t *testing.T) {
	agent := &execLogAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := acceptRAConfig()
	cfg.IPv6.WAN.AcceptRA = false
	NewIPv6Service(cfg).ApplyAcceptRA(context.Background())
	if len(agent.lines) != 0 {
		t.Errorf("sysctl ran with acceptRA off: %v", agent.lines)
	}
}
