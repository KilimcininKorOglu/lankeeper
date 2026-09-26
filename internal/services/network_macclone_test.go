package services

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

func macCloneService(t *testing.T, current string) (*NetworkService, *execLogAgent) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "enp1s0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "enp1s0", "address"), []byte(current+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := sysClassNet
	sysClassNet = dir
	t.Cleanup(func() { sysClassNet = prev })

	agent := &execLogAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{{ID: "wan", Device: "enp1s0", Role: "wan", CloneMAC: "02:11:22:33:44:55"}}
	return NewNetworkService(cfg), agent
}

// A configured clone is applied when the device does not carry it yet.
func TestMACCloneIsRestored(t *testing.T) {
	svc, agent := macCloneService(t, "52:54:00:aa:bb:cc")
	svc.RestoreMACClones(context.Background())
	if !slices.Contains(agent.lines, "ip link set enp1s0 address 02:11:22:33:44:55") {
		t.Errorf("clone not applied; commands: %v", agent.lines)
	}
}

// A web restart must not bounce the WAN to set the address it has.
func TestMACCloneLeavesAMatchingDeviceAlone(t *testing.T) {
	svc, agent := macCloneService(t, "02:11:22:33:44:55")
	svc.RestoreMACClones(context.Background())
	if len(agent.lines) != 0 {
		t.Errorf("device already carrying the clone was touched: %v", agent.lines)
	}
}
