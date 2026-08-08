package services_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func interfaceTestService(t *testing.T) (*services.NetworkService, *config.Config) {
	t.Helper()

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan", Type: "pppoe", MTU: 1492},
		{ID: "lan", Device: "enp0s25", Role: "lan", Type: "static", Address: "10.10.10.1/24", MTU: 1500},
	}
	return services.NewNetworkService(cfg), cfg
}

// Removing the only LAN interface leaves nothing listening on an
// address the operator can reach, and the web UI is the only way back
// in. The refusal has to happen before the config is written, or the
// recovery path is a console cable.
func TestRemoveLastLANInterfaceIsRefused(t *testing.T) {
	svc, cfg := interfaceTestService(t)

	err := svc.RemoveInterface("lan")
	if !errors.Is(err, services.ErrLastLANInterface) {
		t.Fatalf("RemoveInterface(lan) error = %v, want ErrLastLANInterface", err)
	}
	if len(cfg.Interfaces) != 2 {
		t.Errorf("the refused removal still changed the config: %+v", cfg.Interfaces)
	}
}

// Reassigning the only LAN interface to WAN is the same lockout by a
// different route, so it has to be refused in the same place.
func TestReassigningLastLANInterfaceIsRefused(t *testing.T) {
	svc, cfg := interfaceTestService(t)

	err := svc.SetInterface(config.InterfaceConfig{
		ID: "lan", Device: "enp0s25", Role: "wan", Type: "dhcp-client", MTU: 1500,
	})
	if !errors.Is(err, services.ErrLastLANInterface) {
		t.Fatalf("SetInterface(lan->wan) error = %v, want ErrLastLANInterface", err)
	}
	if cfg.Interfaces[1].Role != "lan" {
		t.Errorf("the refused reassignment still changed the config: %+v", cfg.Interfaces[1])
	}
}

// With a second LAN present the guard must not fire, or the operator
// cannot reorganise their network at all.
func TestLastLANGuardAllowsRemovalWhenAnotherLANExists(t *testing.T) {
	svc, cfg := interfaceTestService(t)

	if err := svc.SetInterface(config.InterfaceConfig{
		ID: "lan2", Device: "enp4s0", Role: "lan", Type: "static", Address: "10.10.20.1/24", MTU: 1500,
	}); err != nil {
		t.Fatalf("add second LAN: %v", err)
	}
	if err := svc.RemoveInterface("lan"); err != nil {
		t.Fatalf("removing one of two LAN interfaces was refused: %v", err)
	}
	if len(cfg.Interfaces) != 2 {
		t.Errorf("config = %+v, want wan and lan2", cfg.Interfaces)
	}
}

// The device name reaches nftables, dnsmasq and the PPPoE peer file
// through text/template, which escapes nothing.
func TestSetInterfaceRejectsMalformedValues(t *testing.T) {
	cases := []struct {
		name string
		in   config.InterfaceConfig
	}{
		{"empty device", config.InterfaceConfig{ID: "x", Device: "", Role: "lan", Type: "static"}},
		{"device with quote", config.InterfaceConfig{ID: "x", Device: `eth0"`, Role: "lan", Type: "static"}},
		{"device with newline", config.InterfaceConfig{ID: "x", Device: "eth0\nfoo", Role: "lan", Type: "static"}},
		{"unknown role", config.InterfaceConfig{ID: "x", Device: "eth0", Role: "dmz", Type: "static"}},
		{"unknown type", config.InterfaceConfig{ID: "x", Device: "eth0", Role: "lan", Type: "bridge"}},
		{"bad address", config.InterfaceConfig{ID: "x", Device: "eth0", Role: "lan", Type: "static", Address: "10.10.10.1"}},
		{"bad mtu", config.InterfaceConfig{ID: "x", Device: "eth0", Role: "lan", Type: "static", MTU: 70000}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, cfg := interfaceTestService(t)
			before := len(cfg.Interfaces)

			if err := svc.SetInterface(tc.in); err == nil {
				t.Fatal("accepted, so the value reaches a rendered config file")
			}
			if len(cfg.Interfaces) != before {
				t.Errorf("a rejected entry still reached the config: %+v", cfg.Interfaces)
			}
		})
	}
}

// Two entries naming one device render that NIC twice with conflicting
// settings, and which one wins depends on iteration order.
func TestSetInterfaceRejectsDuplicateDevice(t *testing.T) {
	svc, _ := interfaceTestService(t)

	err := svc.SetInterface(config.InterfaceConfig{
		ID: "wan2", Device: "enp0s25", Role: "wan", Type: "dhcp-client", MTU: 1500,
	})
	if !errors.Is(err, services.ErrDuplicateInterface) {
		t.Fatalf("error = %v, want ErrDuplicateInterface", err)
	}
}

// The case the whole feature exists for: the shipped config names the
// build machine's cards, and the operator has to be able to point them
// at the devices their own hardware actually has.
func TestSetInterfaceCorrectsShippedDeviceNames(t *testing.T) {
	svc, cfg := interfaceTestService(t)

	if err := svc.SetInterface(config.InterfaceConfig{
		ID: "wan", Device: "eno1", Role: "wan", Type: "pppoe", MTU: 1492,
	}); err != nil {
		t.Fatalf("correcting the WAN device was refused: %v", err)
	}
	if cfg.Interfaces[0].Device != "eno1" {
		t.Errorf("device = %q, want eno1", cfg.Interfaces[0].Device)
	}
	if len(cfg.Interfaces) != 2 {
		t.Errorf("the update added an entry instead of replacing one: %+v", cfg.Interfaces)
	}
}
