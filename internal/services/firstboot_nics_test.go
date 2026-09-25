package services

import (
	"context"
	"slices"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// TestPhysicalNICNamesSkipsVirtualAndLoopback pins which interfaces the
// first-boot bridge takes.
func TestPhysicalNICNamesSkipsVirtualAndLoopback(t *testing.T) {
	got := physicalNICNames([]netutil.InterfaceInfo{
		{Name: "lo"}, {Name: "eth0"}, {Name: "wg0", IsVirtual: true}, {Name: "enp2s0"},
	})
	if !slices.Equal(got, []string{"eth0", "enp2s0"}) {
		t.Errorf("got %q", got)
	}
}

// TestEnslaveNICIssuesTheBridgeCommands pins the command order that
// moves a NIC into the first-boot bridge.
func TestEnslaveNICIssuesTheBridgeCommands(t *testing.T) {
	agent := &cmdLogAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	if !enslaveNIC(context.Background(), "eth0") {
		t.Fatal("enslave reported a failure")
	}
	want := []string{"ip addr flush dev eth0", "ip link set eth0 up", "ip link set eth0 master " + firstBootBridge}
	if !slices.Equal(agent.argv, want) {
		t.Errorf("commands = %q, want %q", agent.argv, want)
	}
}
