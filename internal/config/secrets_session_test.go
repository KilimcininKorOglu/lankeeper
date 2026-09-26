package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// The session secret signs every admin cookie and a preshared key stands
// in for a WireGuard key, so neither may reach router.yaml in the clear,
// and both must come back usable on the next load.
func TestSaveEncryptsSessionSecretAndPresharedKeys(t *testing.T) {
	cfgPath, _ := secretsEnv(t)
	cfg := config.DefaultConfig()
	cfg.SetFilePath(cfgPath)
	cfg.System.SessionSecret = "session-signing-secret"
	cfg.VPN.Server.Peers = []config.WGServerPeer{{Name: "phone", PresharedKey: "peer-psk"}}
	cfg.VPN.Clients = []config.WGClientTunnel{{Name: "vpn", PrivateKey: "client-private", PresharedKey: "client-psk"}}

	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	secrets := []string{"session-signing-secret", "peer-psk", "client-private", "client-psk"}
	for _, s := range secrets {
		if strings.Contains(string(raw), s) {
			t.Errorf("%q is on disk in the clear", s)
		}
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := []string{loaded.System.SessionSecret, loaded.VPN.Server.Peers[0].PresharedKey,
		loaded.VPN.Clients[0].PrivateKey, loaded.VPN.Clients[0].PresharedKey}
	for i, s := range secrets {
		if got[i] != s {
			t.Errorf("loaded %q, want %q", got[i], s)
		}
	}
	if cfg.VPN.Clients[0].PrivateKey != "client-private" {
		t.Error("saving rewrote the live config's client key")
	}
}
