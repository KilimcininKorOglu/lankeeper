package services

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

func TestValidateVLANRefusesAServedSubnet(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{{ID: "lan", Device: "eth1", Role: "lan", Address: "10.10.10.1/24"}}
	cfg.VLANs = []config.VLANConfig{{ID: "guest", Parent: "lan", VID: 13, Address: "10.10.13.1/24"}}
	cfg.VPN.Server.Address = "10.10.11.1/24"
	cfg.OpenVPN.Server.Subnet = "10.8.0.0/24"
	svc := NewNetworkService(cfg)

	base := config.VLANConfig{ID: "iot", Parent: "lan", VID: 20, Label: "iot", Role: "lan", Type: "ethernet"}
	for _, addr := range []string{"10.10.10.254/24", "10.10.13.9/24", "10.10.11.1/24", "10.8.0.1/24", "10.0.0.1/8"} {
		v := base
		v.Address = addr
		if err := svc.validateVLANSubnet(v); err == nil {
			t.Errorf("VLAN at %s accepted although the router already serves that subnet", addr)
		}
	}
	v := base
	v.Address = "10.10.20.1/24"
	if err := svc.validateVLANSubnet(v); err != nil {
		t.Errorf("a free subnet was refused: %v", err)
	}
}
