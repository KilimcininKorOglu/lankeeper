package services

import (
	"context"
	"errors"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// The Enabled and Auto failover switches must stop the health-check
// chain from moving the default route onto the USB link.
func TestFailoverUSBHonoursTheOperatorSwitches(t *testing.T) {
	cases := []struct{ enabled, auto bool }{{false, false}, {true, false}, {false, true}}
	for _, c := range cases {
		agent := &refusingAgent{}
		netutil.SetAgentClient(agent)
		cfg := config.DefaultConfig()
		cfg.USBTether.Enabled = c.enabled
		cfg.USBTether.AutoFailover = c.auto
		err := (&HealthCheckService{cfg: cfg}).actionFailoverUSB(context.Background())
		netutil.SetAgentClient(nil)
		if !errors.Is(err, errUSBFailoverDisabled) || len(agent.calls) != 0 {
			t.Errorf("enabled=%v auto=%v: err=%v agent calls=%v, want refusal before any command", c.enabled, c.auto, err, agent.calls)
		}
	}
}
