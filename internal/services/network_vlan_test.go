package services

import (
	"errors"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

func vlanTestService() *NetworkService {
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan", Device: "eth1", Role: "lan", Type: "static", Address: "10.10.10.1/24"},
	}
	cfg.VLANs = []config.VLANConfig{{ID: "iot", Parent: "lan", VID: 30, Role: "lan", Type: "static"}}
	return NewNetworkService(cfg)
}

func validVLAN() config.VLANConfig {
	return config.VLANConfig{
		ID: "guest", Parent: "lan", VID: 20, Label: "Guest", Role: "lan", Type: "static",
		Address: "10.10.20.1/24", MTU: 1500,
		DHCP: config.VLANDHCPConfig{Enabled: true, RangeStart: "10.10.20.100", RangeEnd: "10.10.20.200", LeaseTime: "12h"},
	}
}

func TestValidateVLANAcceptsAFullEntry(t *testing.T) {
	if err := vlanTestService().ValidateVLAN(validVLAN()); err != nil {
		t.Fatalf("a valid VLAN was refused: %v", err)
	}
}

// TestValidateVLANRefusesEveryBadField is the regression test. The form
// fields went into the config unchecked. The DHCP range and lease time
// reach dnsmasq.conf through text/template, which escapes nothing, so a
// newline added a dnsmasq option such as dhcp-script, which dnsmasq runs
// as root. An empty or unknown parent is now also a config Load refuses.
func TestValidateVLANRefusesEveryBadField(t *testing.T) {
	for label, mutate := range map[string]func(*config.VLANConfig){
		"id":             func(v *config.VLANConfig) { v.ID = "a b" },
		"duplicate id":   func(v *config.VLANConfig) { v.ID = "iot" },
		"parent":         func(v *config.VLANConfig) { v.Parent = "" },
		"unknown parent": func(v *config.VLANConfig) { v.Parent = "nope" },
		"vid":            func(v *config.VLANConfig) { v.VID = 4095 },
		"duplicate vid":  func(v *config.VLANConfig) { v.VID = 30 },
		"label":          func(v *config.VLANConfig) { v.Label = "x\ny" },
		"role":           func(v *config.VLANConfig) { v.Role = "dmz" },
		"type":           func(v *config.VLANConfig) { v.Type = "bridge" },
		"address":        func(v *config.VLANConfig) { v.Address = "10.10.20.1" },
		"mtu":            func(v *config.VLANConfig) { v.MTU = 20 },
		"range start":    func(v *config.VLANConfig) { v.DHCP.RangeStart = "10.10.20.100\ndhcp-script=/tmp/x" },
		"range outside":  func(v *config.VLANConfig) { v.DHCP.RangeEnd = "10.10.99.200" },
		"lease time":     func(v *config.VLANConfig) { v.DHCP.LeaseTime = "12h\ndhcp-script=/tmp/x" },
		"dhcp no addr":   func(v *config.VLANConfig) { v.Address = ""; v.DHCP.RangeStart = ""; v.DHCP.RangeEnd = "" },
	} {
		v := validVLAN()
		mutate(&v)
		if err := vlanTestService().ValidateVLAN(v); !errors.Is(err, ErrInvalidVLAN) {
			t.Errorf("%s: got %v, want ErrInvalidVLAN", label, err)
		}
	}
}
