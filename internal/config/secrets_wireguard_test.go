package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// wgSecretsConfig writes a config carrying WireGuard key material and
// returns its path, with the credential key redirected under the test's
// own directory.
func wgSecretsConfig(t *testing.T) (*config.Config, string) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(dir, "config.key"))

	path := filepath.Join(dir, "router.yaml")
	cfg := &config.Config{}
	cfg.SetFilePath(path)
	cfg.VPN.Server.PrivateKey = "SERVER-PRIVATE-KEY"
	cfg.VPN.Server.PublicKey = "SERVER-PUBLIC-KEY"
	cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "phone", PublicKey: "PEER-PUB-1", PrivateKey: "PEER-PRIVATE-1"},
		{Name: "laptop", PublicKey: "PEER-PUB-2", PrivateKey: "PEER-PRIVATE-2"},
	}
	return cfg, path
}

// The whole point of encrypting at rest is that the file no longer
// carries the key. A config copied off the box for debugging must not
// hand over the tunnel.
func TestWireGuardKeysAreCiphertextOnDisk(t *testing.T) {
	cfg, path := wgSecretsConfig(t)

	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(path) // #nosec G304 -- test-owned path
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	body := string(raw)

	for _, secret := range []string{"SERVER-PRIVATE-KEY", "PEER-PRIVATE-1", "PEER-PRIVATE-2"} {
		if strings.Contains(body, secret) {
			t.Errorf("%s is on disk in the clear:\n%s", secret, body)
		}
	}
	// The public keys are not secrets and must stay readable, or a
	// blanket encrypt would have been applied to the wrong fields.
	if !strings.Contains(body, "SERVER-PUBLIC-KEY") {
		t.Error("the server public key was encrypted, but it is not a secret")
	}
}

// Encrypting must not disturb the config the process is running on.
// withEncryptedSecrets starts from a shallow copy, so a slice reached
// through it still points at the caller's backing array; rewriting a
// peer in place there would leave the live config holding ciphertext
// where wg-quick expects a key.
func TestSavingLeavesTheLiveConfigUsable(t *testing.T) {
	cfg, _ := wgSecretsConfig(t)

	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	if cfg.VPN.Server.PrivateKey != "SERVER-PRIVATE-KEY" {
		t.Errorf("server key in memory = %q, want the plaintext; the running process cannot bring the tunnel up",
			cfg.VPN.Server.PrivateKey)
	}
	for i, want := range []string{"PEER-PRIVATE-1", "PEER-PRIVATE-2"} {
		if got := cfg.VPN.Server.Peers[i].PrivateKey; got != want {
			t.Errorf("peer %d key in memory = %q, want %q", i, got, want)
		}
	}
}

func TestWireGuardKeysSurviveARoundTrip(t *testing.T) {
	cfg, path := wgSecretsConfig(t)

	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.VPN.Server.PrivateKey != "SERVER-PRIVATE-KEY" {
		t.Errorf("server key = %q after reload", loaded.VPN.Server.PrivateKey)
	}
	if len(loaded.VPN.Server.Peers) != 2 {
		t.Fatalf("peers = %d, want 2", len(loaded.VPN.Server.Peers))
	}
	for i, want := range []string{"PEER-PRIVATE-1", "PEER-PRIVATE-2"} {
		if got := loaded.VPN.Server.Peers[i].PrivateKey; got != want {
			t.Errorf("peer %d key = %q after reload, want %q", i, got, want)
		}
	}
}

// Losing the credential key must not stop the router. It is the DNS,
// DHCP and firewall for the network, so refusing to load would take
// those down to protect a key that is already gone. The affected fields
// are cleared and reported instead.
func TestUnreadableKeyClearsRatherThanBlocksTheLoad(t *testing.T) {
	cfg, path := wgSecretsConfig(t)
	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Point at a key that was never written, which is what a restored
	// config without its key directory looks like.
	t.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(t.TempDir(), "missing.key"))

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load refused to proceed without the credential key: %v", err)
	}
	if loaded.VPN.Server.PrivateKey != "" {
		t.Errorf("server key = %q, want it cleared rather than left as ciphertext",
			loaded.VPN.Server.PrivateKey)
	}
	for i, p := range loaded.VPN.Server.Peers {
		if p.PrivateKey != "" {
			t.Errorf("peer %d key = %q, want it cleared", i, p.PrivateKey)
		}
		// The peer itself still works, because the server only needs
		// its public key. Only re-issuing its config is lost.
		if p.PublicKey == "" {
			t.Errorf("peer %d lost its public key, so the tunnel breaks entirely", i)
		}
	}
}

// A config written before this field existed carries no ciphertext and
// must keep loading, with the peer intact and simply not re-issuable.
func TestPeerWithoutAStoredKeyStillLoads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(dir, "config.key"))
	path := filepath.Join(dir, "router.yaml")

	cfg := &config.Config{}
	cfg.SetFilePath(path)
	cfg.VPN.Server.Peers = []config.WGServerPeer{
		{Name: "legacy", PublicKey: "PEER-PUB", AllowedIPs: "10.10.11.2/32"},
	}
	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.VPN.Server.Peers) != 1 {
		t.Fatalf("peers = %d, want the legacy peer preserved", len(loaded.VPN.Server.Peers))
	}
	if loaded.VPN.Server.Peers[0].PublicKey != "PEER-PUB" {
		t.Error("the legacy peer lost its public key")
	}
	if loaded.VPN.Server.Peers[0].PrivateKey != "" {
		t.Error("a private key appeared for a peer that never had one stored")
	}
}
