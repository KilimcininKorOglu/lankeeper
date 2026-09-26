package services

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

func nasBindConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "enp1s0", Role: "wan", Address: "203.0.113.7/24"},
		{ID: "lan", Device: "enp2s0", Role: "lan", Address: "10.10.10.1/24"},
	}
	cfg.VLANs = []config.VLANConfig{{Parent: "lan", VID: 13, Address: "10.10.13.1/24"}}
	cfg.NAS.Shares = []config.ShareConfig{{Name: "media", Path: "/srv/media"}}
	return cfg
}

// Samba must not rely on the firewall to stay off the WAN: it binds to
// the router's LAN and VLAN addresses only and admits only served
// networks.
func TestSambaListensOnlyOnTheLAN(t *testing.T) {
	t.Chdir("../..")
	out, err := NewNASService(nasBindConfig()).RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"interfaces = 127.0.0.1/8 10.10.10.1/24 10.10.13.1/24\n",
		"bind interfaces only = yes\n",
		"hosts allow = 127.0.0.1 10.10.10.0/24 10.10.13.0/24",
		"hosts deny = ALL\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("smb.conf lacks %q", want)
		}
	}
	if strings.Contains(out, "203.0.113") {
		t.Error("smb.conf names the WAN address")
	}
}

// A file server with nothing to serve is only something listening, so
// Samba runs only while a share exists.
func TestSambaRunsOnlyWithAShare(t *testing.T) {
	t.Chdir("../..")
	for _, tc := range []struct {
		shares []config.ShareConfig
		verb   string
	}{
		{nil, "disable"},
		{[]config.ShareConfig{{Name: "media", Path: "/srv/media"}}, "enable"},
	} {
		agent := &execLogAgent{}
		netutil.SetAgentClient(agent)
		cfg := nasBindConfig()
		cfg.NAS.Shares = tc.shares
		if err := NewNASService(cfg).ApplyConfig(context.Background()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		netutil.SetAgentClient(nil)
		for _, unit := range []string{"smbd", "nmbd"} {
			if want := "systemctl " + tc.verb + " --now " + unit; !slices.Contains(agent.lines, want) {
				t.Errorf("shares=%d: missing %q; commands: %v", len(tc.shares), want, agent.lines)
			}
		}
	}
}
