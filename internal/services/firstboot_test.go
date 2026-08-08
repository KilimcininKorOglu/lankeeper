package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// firstBootFlag points the service at a marker under the test's own
// directory, since /var/lib is not writable from a test.
func firstBootFlag(t *testing.T, present bool) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), ".first-boot")
	t.Setenv("LANKEEPER_FIRST_BOOT_FLAG", path)

	if present {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("seed flag: %v", err)
		}
	}
	return path
}

func firstBootConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp3s0", Role: "wan"},
		{ID: "wan2", Device: "enp5s0", Role: "wan"},
		{ID: "lan", Device: "enp0s25", Role: "lan"},
	}
	return cfg
}

func TestFirstBootIsActiveFollowsTheMarker(t *testing.T) {
	svc := services.NewFirstBootService(firstBootConfig())

	firstBootFlag(t, false)
	if svc.IsActive() {
		t.Error("reported active with no marker on disk")
	}

	firstBootFlag(t, true)
	if !svc.IsActive() {
		t.Error("reported inactive while the installer's marker is present")
	}
}

// The banner tells the operator which cards the button will detach, so
// the list has to be the WAN entries and nothing else. Showing a LAN
// card there would promise to cut the connection they are using.
func TestFirstBootWANDevicesListsOnlyWANEntries(t *testing.T) {
	svc := services.NewFirstBootService(firstBootConfig())

	got := svc.WANDevices()
	want := map[string]bool{"enp3s0": true, "enp5s0": true}

	if len(got) != len(want) {
		t.Fatalf("WANDevices() = %v, want exactly the two WAN devices", got)
	}
	for _, dev := range got {
		if !want[dev] {
			t.Errorf("WANDevices() included %q, which is not a WAN interface", dev)
		}
	}
}

// Complete is the only thing that ends first-boot mode, and the mode is
// an exposure while it lasts: the bridge carries the WAN card. If the
// marker survived, the next start would rebuild the bridge and put the
// operator back into it without saying so.
func TestFirstBootCompleteClearsTheMarker(t *testing.T) {
	path := firstBootFlag(t, true)
	svc := services.NewFirstBootService(firstBootConfig())

	if err := svc.Complete(context.Background()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the marker survived, so the next start rebuilds the bridge: %v", err)
	}
	if svc.IsActive() {
		t.Error("still reports active after completing")
	}
}

// Completing twice, or completing when the installer never set the
// marker, has to refuse rather than report success. A silent success
// would tell the operator the exposure ended when nothing checked.
func TestFirstBootCompleteRefusesWhenNotActive(t *testing.T) {
	firstBootFlag(t, false)
	svc := services.NewFirstBootService(firstBootConfig())

	if err := svc.Complete(context.Background()); err == nil {
		t.Error("completing an inactive first-boot reported success")
	}
}
