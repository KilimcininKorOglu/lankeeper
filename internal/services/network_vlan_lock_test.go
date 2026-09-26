package services

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// Two concurrent adds of the same VLAN each validated against a list the
// other had not stored yet, so both passed and the second store dropped
// the first. The check, the swap and the save now run as one step.
func TestConcurrentVLANAddsKeepOneEntry(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{{ID: "lan0", Device: "eth1", Role: "lan", Address: "10.10.10.1/24"}}
	svc := NewNetworkService(cfg)
	vlan := config.VLANConfig{ID: "iot", Parent: "lan0", VID: 20, Label: "IoT", Role: "lan", Type: "static", Address: "10.10.20.1/24", MTU: 1500}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _ = svc.AddVLAN(vlan) })
	}
	wg.Go(func() {
		for range 8 {
			_, _, _ = svc.RemoveVLAN("absent")
		}
	})
	wg.Wait()

	if n := len(cfg.VLANs); n != 1 {
		t.Fatalf("%d VLANs stored, want 1: %+v", n, cfg.VLANs)
	}
	if _, found, err := svc.RemoveVLAN("iot"); !found || err != nil {
		t.Errorf("remove iot: found %v, err %v", found, err)
	}
	if len(cfg.VLANs) != 0 {
		t.Errorf("VLANs after remove: %+v", cfg.VLANs)
	}
}
