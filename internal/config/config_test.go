package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// TestLoadRefusesAnInvalidConfig is the regression test. Validate
// existed but nothing called it, so a config naming a QoS profile or an
// interface role no service understands loaded without complaint and
// failed later, far from its cause.
func TestLoadRefusesAnInvalidConfig(t *testing.T) {
	t.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(t.TempDir(), "config.key"))
	path := filepath.Join(t.TempDir(), "router.yaml")
	cfg := config.DefaultConfig()
	cfg.QoS.Profile = "gaming"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "qos.profile") {
		t.Fatalf("Load = %v, want an error naming qos.profile", err)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	original := &config.Config{}
	original.System.Hostname = "test-router"
	original.System.Language = "tr"
	original.System.WebPort = 8443
	original.System.WebBind = "10.10.10.1"
	original.System.TLS.Mode = "self-signed"

	if err := config.Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.System.Hostname != "test-router" {
		t.Errorf("hostname = %q, want %q", loaded.System.Hostname, "test-router")
	}
	if loaded.System.WebPort != 8443 {
		t.Errorf("webPort = %d, want %d", loaded.System.WebPort, 8443)
	}
	if loaded.System.TLS.Mode != "self-signed" {
		t.Errorf("tls.mode = %q, want %q", loaded.System.TLS.Mode, "self-signed")
	}
}

func TestAtomicWriteNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.yaml")

	cfg := &config.Config{}
	cfg.System.Hostname = "atomic-test"

	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	for _, e := range entries {
		if e.Name() != "atomic.yaml" {
			t.Errorf("unexpected file in dir: %s (temp file not cleaned up?)", e.Name())
		}
	}
}

func TestLoadDefaultConfig(t *testing.T) {
	loaded, err := config.Load("../../configs/defaults/router.yaml")
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}

	if loaded.System.Hostname != "hermes" {
		t.Errorf("hostname = %q, want %q", loaded.System.Hostname, "hermes")
	}
	if loaded.PPPoE.MTU != 1492 {
		t.Errorf("pppoe.mtu = %d, want 1492", loaded.PPPoE.MTU)
	}
	if loaded.DHCP.RangeStart != "10.10.10.100" {
		t.Errorf("dhcp.rangeStart = %q, want 10.10.10.100", loaded.DHCP.RangeStart)
	}
	if loaded.VPN.Server.Address != "10.10.11.1/24" {
		t.Errorf("vpn.server.address = %q, want 10.10.11.1/24", loaded.VPN.Server.Address)
	}
}
