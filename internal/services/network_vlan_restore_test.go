package services

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// execLogAgent records every exec.run command line and succeeds.
type execLogAgent struct{ lines []string }

func (a *execLogAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method == "exec.run" {
		raw, _ := json.Marshal(params)
		var p struct {
			Cmd  string   `json:"cmd"`
			Args []string `json:"args"`
		}
		_ = json.Unmarshal(raw, &p)
		a.lines = append(a.lines, p.Cmd+" "+strings.Join(p.Args, " "))
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func TestRestoreVLANsRecreatesAMissingDevice(t *testing.T) {
	agent := &execLogAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{{ID: "lan", Device: "lkt-nosuchdev", Role: "lan"}}
	cfg.VLANs = []config.VLANConfig{{Parent: "lan", VID: 20, Address: "10.10.20.1/24"}}

	if err := NewNetworkService(cfg).RestoreVLANs(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := "ip link add link lkt-nosuchdev name lkt-nosuchdev.20 type vlan id 20"
	for _, l := range agent.lines {
		if l == want {
			return
		}
	}
	t.Fatalf("device not recreated; commands: %v", agent.lines)
}

// Nothing but Serve can recreate the devices after a reboot.
func TestServeRestoresVLANs(t *testing.T) {
	src, err := os.ReadFile("../web/server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "s.networkSvc.RestoreVLANs(ctx)") {
		t.Fatal("Serve does not restore the VLAN devices")
	}
}
