package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// fileWriteAgent keeps the content of every file.write by path and
// answers everything else with success.
type fileWriteAgent struct {
	mu    sync.Mutex
	files map[string]string
}

func (a *fileWriteAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "file.write" {
		return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.files[p.Path] = p.Content
	a.mu.Unlock()
	return []byte(`{"status":"ok"}`), nil
}

// TestOpenVPNAddClientRefusesAnAddressItCannotPush is the regression
// test. The handler checked only that the value parsed as an address, so
// an IPv6 address or one outside the server subnet was stored and then
// never pushed, and the client silently received a pool address.
func TestOpenVPNAddClientRefusesAnAddressItCannotPush(t *testing.T) {
	agent := &fileWriteAgent{files: map[string]string{}}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.OpenVPN.Server.Subnet = "10.8.0.0/24"
	svc := NewOpenVPNService(cfg)

	for _, ip := range []string{"192.168.1.20", "fd00::20", "10.8.0.0", "10.8.0.1", "10.8.0.255"} {
		err := svc.AddClient(context.Background(), "laptop", false, nil, ip)
		if !errors.Is(err, ErrInvalidFixedIP) {
			t.Errorf("fixed address %s: err = %v, want ErrInvalidFixedIP", ip, err)
		}
	}
	if len(cfg.OpenVPN.Server.Clients) != 0 {
		t.Errorf("a refused client was stored: %+v", cfg.OpenVPN.Server.Clients)
	}
	if err := svc.validateFixedIP("10.8.0.20"); err != nil {
		t.Errorf("a host address inside the subnet was refused: %v", err)
	}
}

// TestOpenVPNFixedAddressMatchesTheServerTopology is the regression
// test. The server ran OpenVPN's default net30 topology, where the
// second argument of ifconfig-push is the peer endpoint, while the CCD
// pushed an address and a /24 mask whatever the server subnet was. A
// client given a fixed address came up with an unusable interface.
func TestOpenVPNFixedAddressMatchesTheServerTopology(t *testing.T) {
	t.Chdir("../..")
	agent := &fileWriteAgent{files: map[string]string{}}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.OpenVPN.Server.Subnet = "10.8.0.0/16"
	cfg.OpenVPN.Server.Clients = []config.OVPNClientEntry{
		{Name: "laptop", CommonName: "laptop", Enabled: true, FixedIP: "10.8.3.7"},
	}
	if err := NewOpenVPNService(cfg).RenderServerConfig(); err != nil {
		t.Fatalf("render: %v", err)
	}

	server := agent.files["/etc/openvpn/server.conf"]
	if !strings.Contains(server, "topology subnet\nserver 10.8.0.0 255.255.0.0\n") {
		t.Errorf("server.conf does not run topology subnet:\n%s", server)
	}
	if ccd := agent.files["/etc/openvpn/ccd/laptop"]; ccd != "ifconfig-push 10.8.3.7 255.255.0.0\n" {
		t.Errorf("ccd = %q, want the address with the server subnet mask", ccd)
	}
}
