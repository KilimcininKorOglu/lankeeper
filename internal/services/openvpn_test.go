package services_test

import (
	"context"
	"testing"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
)

func TestNewOpenVPNService(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewOpenVPNService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestOpenVPNServerStatus(t *testing.T) {
	cfg := &config.Config{}
	cfg.OpenVPN.Server.Enabled = false
	cfg.OpenVPN.Server.Port = 1194
	cfg.OpenVPN.Server.Protocol = "udp"

	svc := services.NewOpenVPNService(cfg)
	status, err := svc.ServerStatus(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if status.Enabled {
		t.Error("should not be enabled")
	}
	if status.Active {
		t.Error("should not be active when openvpn is not running")
	}
}

func TestOpenVPNImportClient(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewOpenVPNService(cfg)

	svc.ImportClientConfig("work-vpn", "client\ndev tun\nproto udp\n")

	if len(cfg.OpenVPN.Clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(cfg.OpenVPN.Clients))
	}
	if cfg.OpenVPN.Clients[0].Name != "work-vpn" {
		t.Errorf("name = %q, want work-vpn", cfg.OpenVPN.Clients[0].Name)
	}
}

func TestOpenVPNListServerClients(t *testing.T) {
	cfg := &config.Config{}
	cfg.OpenVPN.Server.Clients = []config.OVPNClientEntry{
		{Name: "laptop", CommonName: "laptop", Enabled: true},
		{Name: "remote-office", CommonName: "remote-office", Enabled: true, IsSiteToSite: true, RemoteSubnets: []string{"192.168.2.0/24"}},
	}

	svc := services.NewOpenVPNService(cfg)
	clients := svc.ListServerClients()

	if len(clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(clients))
	}
	if !clients[1].IsSiteToSite {
		t.Error("second client should be site-to-site")
	}
	if len(clients[1].RemoteSubnets) != 1 || clients[1].RemoteSubnets[0] != "192.168.2.0/24" {
		t.Errorf("remote subnets mismatch: %v", clients[1].RemoteSubnets)
	}
}

func TestOpenVPNCIDRToIPMask(t *testing.T) {
	cfg := &config.Config{}
	cfg.OpenVPN.Server.Enabled = true
	cfg.OpenVPN.Server.Subnet = "10.8.0.0/24"
	cfg.OpenVPN.Server.Protocol = "udp"
	cfg.OpenVPN.Server.Port = 1194
	cfg.OpenVPN.Server.Device = "tun0"
	cfg.OpenVPN.Server.Cipher = "AES-256-GCM"
	cfg.OpenVPN.Server.Auth = "SHA256"
	cfg.OpenVPN.Server.TLSAuth = true
	cfg.OpenVPN.Server.Keepalive = "10 120"
	cfg.OpenVPN.Server.MaxClients = 10
	cfg.OpenVPN.Server.DNS = "10.10.10.1"

	svc := services.NewOpenVPNService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestOpenVPNSiteToSiteClientEntry(t *testing.T) {
	entry := config.OVPNClientEntry{
		Name:          "branch-office",
		CommonName:    "branch-office",
		Enabled:       true,
		IsSiteToSite:  true,
		RemoteSubnets: []string{"192.168.2.0/24", "172.16.0.0/16"},
		FixedIP:       "10.8.0.10",
	}

	if !entry.IsSiteToSite {
		t.Error("should be site-to-site")
	}
	if len(entry.RemoteSubnets) != 2 {
		t.Errorf("expected 2 subnets, got %d", len(entry.RemoteSubnets))
	}
	if entry.FixedIP != "10.8.0.10" {
		t.Errorf("fixed IP = %q, want 10.8.0.10", entry.FixedIP)
	}
}
