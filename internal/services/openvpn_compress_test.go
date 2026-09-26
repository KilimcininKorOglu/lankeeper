package services

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

func renderOVPNPair(t *testing.T, compression bool) (server, client string) {
	t.Helper()
	agent := &pkiReadAgent{fileWriteAgent{files: map[string]string{}}}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.OpenVPN.Server.Compression = compression
	svc := NewOpenVPNService(cfg)
	if err := svc.RenderServerConfig(); err != nil {
		t.Fatalf("render: %v", err)
	}
	client, err := svc.GenerateClientOVPN("laptop")
	if err != nil {
		t.Fatalf("client profile: %v", err)
	}
	return agent.files["/etc/openvpn/server.conf"], client
}

func hasLine(text, line string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// The server and every profile it hands out must agree on whether data
// packets carry a compression header, or the tunnel passes no traffic.
func TestOpenVPNCompressionFramingMatchesOnBothEnds(t *testing.T) {
	t.Chdir("../..")
	for _, on := range []bool{false, true} {
		server, client := renderOVPNPair(t, on)
		if hasLine(server, "compress") != on {
			t.Errorf("compression=%v: server.conf compress line present=%v", on, !on)
		}
		if hasLine(client, "compress") != on {
			t.Errorf("compression=%v: client profile compress line present=%v", on, !on)
		}
	}
}
