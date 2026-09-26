package services

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// fileWriteRecorder keeps the content of every file.write by path.
type fileWriteRecorder struct{ files map[string]string }

func (a *fileWriteRecorder) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method == "file.write" {
		raw, _ := json.Marshal(params)
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(raw, &p)
		a.files[p.Path] = p.Content
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

// dhcp6c takes its interface on the command line, so the unit cannot
// start unless the service leaves the WAN device where the unit reads it.
func TestPDFilesNameTheWANInterfaceForTheUnit(t *testing.T) {
	t.Chdir("../..")
	agent := &fileWriteRecorder{files: map[string]string{}}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp1s0", Role: "wan"},
		{ID: "lan", Device: "enp2s0", Role: "lan"},
	}
	if err := NewIPv6Service(cfg).writePDFiles(); err != nil {
		t.Fatalf("write PD files: %v", err)
	}
	if got := agent.files[dhcp6cEnvPath]; got != "DHCP6C_INTERFACE=enp1s0\n" {
		t.Errorf("%s = %q", dhcp6cEnvPath, got)
	}
}
