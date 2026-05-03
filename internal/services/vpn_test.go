package services_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
)

func TestNewVPNService(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewVPNService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestVPNAddRemovePeer(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Server.Enabled = true
	cfg.VPN.Server.ListenPort = 51820
	cfg.VPN.Server.Address = "10.10.11.1/24"
	cfg.VPN.Server.DNS = "10.10.10.1"
	cfg.VPN.Server.PublicKey = "test-server-pubkey"

	svc := services.NewVPNService(cfg)

	peer := config.WGServerPeer{
		Name:       "test-phone",
		PublicKey:  "test-pub-key",
		AllowedIPs: "10.10.11.2/32",
		Keepalive:  25,
	}
	cfg.VPN.Server.Peers = append(cfg.VPN.Server.Peers, peer)

	if len(cfg.VPN.Server.Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(cfg.VPN.Server.Peers))
	}

	if err := svc.RemovePeer("test-phone"); err != nil {
		t.Fatalf("remove peer: %v", err)
	}

	if len(cfg.VPN.Server.Peers) != 0 {
		t.Error("should have 0 peers after removal")
	}
}

func TestVPNRemovePeerNotFound(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	svc := services.NewVPNService(cfg)

	if err := svc.RemovePeer("nonexistent"); err == nil {
		t.Error("should error for nonexistent peer")
	}
}

func TestVPNGeneratePeerConfigRoadWarrior(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Server.ListenPort = 51820
	cfg.VPN.Server.PublicKey = "server-pub-key-base64"
	cfg.VPN.Server.DNS = "10.10.10.1"
	cfg.VPN.Server.MTU = 1420

	svc := services.NewVPNService(cfg)

	peer := &config.WGServerPeer{
		Name:         "laptop",
		PublicKey:    "peer-pub-key",
		PresharedKey: "psk-key",
		AllowedIPs:   "10.10.11.3/32",
		Keepalive:    25,
	}

	confStr := svc.GeneratePeerConfig(peer, "peer-private-key")

	if !strings.Contains(confStr, "PrivateKey = peer-private-key") {
		t.Error("config should contain peer private key")
	}
	if !strings.Contains(confStr, "PublicKey = server-pub-key-base64") {
		t.Error("config should contain server public key")
	}
	if !strings.Contains(confStr, "PresharedKey = psk-key") {
		t.Error("config should contain preshared key")
	}
	if !strings.Contains(confStr, "DNS = 10.10.10.1") {
		t.Error("config should contain DNS")
	}
	if !strings.Contains(confStr, "MTU = 1420") {
		t.Error("config should contain MTU")
	}
	if !strings.Contains(confStr, "AllowedIPs = 0.0.0.0/0, ::/0") {
		t.Error("config should contain full tunnel AllowedIPs")
	}
	if !strings.Contains(confStr, "Address = 10.10.11.3/32") {
		t.Error("config should contain peer address")
	}
}

func TestVPNGeneratePeerConfigSiteToSite(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan", Device: "enp0s25", Role: "lan", Address: "10.10.10.1/24"},
	}
	cfg.VPN.Server.ListenPort = 51820
	cfg.VPN.Server.PublicKey = "server-pub-key-base64"
	cfg.VPN.Server.DNS = "10.10.10.1"
	cfg.VPN.Server.Address = "10.10.11.1/24"
	cfg.VPN.Server.MTU = 1420

	svc := services.NewVPNService(cfg)

	peer := &config.WGServerPeer{
		Name:          "remote-office",
		PublicKey:     "peer-pub-key",
		PresharedKey:  "psk-key",
		AllowedIPs:    "10.10.11.3/32, 192.168.2.0/24",
		Keepalive:     25,
		IsSiteToSite:  true,
		RemoteSubnets: []string{"192.168.2.0/24"},
		Endpoint:      "5.6.7.8:51820",
	}

	confStr := svc.GeneratePeerConfig(peer, "peer-private-key")

	if !strings.Contains(confStr, "Address = 10.10.11.3/32") {
		t.Errorf("S2S config should use tunnel IP as address, got:\n%s", confStr)
	}
	if strings.Contains(confStr, "AllowedIPs = 0.0.0.0/0") {
		t.Error("S2S config should NOT contain full tunnel AllowedIPs")
	}
	if !strings.Contains(confStr, "10.10.11.0/24") {
		t.Errorf("S2S config should contain server WG subnet in AllowedIPs, got:\n%s", confStr)
	}
	if !strings.Contains(confStr, "10.10.10.0/24") {
		t.Errorf("S2S config should contain server LAN subnet in AllowedIPs, got:\n%s", confStr)
	}
	if strings.Contains(confStr, "DNS = ") {
		t.Error("S2S config should NOT contain DNS (remote site has own DNS)")
	}
}

func TestVPNSiteToSitePeerAllowedIPs(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "test-config.yaml"))
	cfg.VPN.Server.Enabled = true
	cfg.VPN.Server.Address = "10.10.11.1/24"

	peer := config.WGServerPeer{
		Name:          "branch-office",
		PublicKey:     "test-pub",
		AllowedIPs:    "10.10.11.2/32, 192.168.2.0/24",
		IsSiteToSite:  true,
		RemoteSubnets: []string{"192.168.2.0/24"},
		Endpoint:      "1.2.3.4:51820",
	}
	cfg.VPN.Server.Peers = append(cfg.VPN.Server.Peers, peer)

	if !peer.IsSiteToSite {
		t.Error("peer should be marked as site-to-site")
	}
	if !strings.Contains(peer.AllowedIPs, "192.168.2.0/24") {
		t.Error("AllowedIPs should contain remote subnet")
	}
	if !strings.Contains(peer.AllowedIPs, "10.10.11.2/32") {
		t.Error("AllowedIPs should contain tunnel IP")
	}
	if len(peer.RemoteSubnets) != 1 || peer.RemoteSubnets[0] != "192.168.2.0/24" {
		t.Errorf("RemoteSubnets mismatch: %v", peer.RemoteSubnets)
	}
}
