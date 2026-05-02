package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

type VPNService struct {
	cfg *config.Config
	mu  sync.RWMutex
}

func NewVPNService(cfg *config.Config) *VPNService {
	return &VPNService{cfg: cfg}
}

type WGTunnelStatus struct {
	Name      string
	Active    bool
	PublicKey string
	Endpoint  string
	Transfer  string
	Handshake string
}

func (s *VPNService) ListClientTunnels(ctx context.Context) ([]WGTunnelStatus, error) {
	var tunnels []WGTunnelStatus
	for i, client := range s.cfg.VPN.Clients {
		iface := fmt.Sprintf("wg%d", i)
		status := WGTunnelStatus{
			Name:     client.Name,
			Endpoint: client.Endpoint,
		}

		out, err := netutil.RunSimple(ctx, "wg", "show", iface)
		if err == nil && strings.Contains(out, "public key") {
			status.Active = true
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "public key:") {
					status.PublicKey = strings.TrimPrefix(line, "public key: ")
				}
				if strings.HasPrefix(line, "transfer:") {
					status.Transfer = strings.TrimPrefix(line, "transfer: ")
				}
				if strings.HasPrefix(line, "latest handshake:") {
					status.Handshake = strings.TrimPrefix(line, "latest handshake: ")
				}
			}
		}

		tunnels = append(tunnels, status)
	}
	return tunnels, nil
}

func (s *VPNService) ConnectClient(ctx context.Context, name string) error {
	idx, client := s.findClient(name)
	if client == nil {
		return fmt.Errorf("tunnel %q not found", name)
	}

	iface := fmt.Sprintf("wg%d", idx)
	confPath := filepath.Join("/etc/wireguard", iface+".conf")

	if err := s.renderClientConfig(client, confPath); err != nil {
		return err
	}

	_, err := netutil.Run(ctx, "wg-quick", "up", iface)
	if err != nil {
		return fmt.Errorf("wg-quick up %s: %w", iface, err)
	}

	_, err = netutil.Run(ctx, "ip", "route", "add", "default", "dev", iface,
		"table", fmt.Sprintf("%d", client.Table))
	if err != nil {
		log.Printf("add route table %d: %v", client.Table, err)
	}

	_, err = netutil.Run(ctx, "ip", "rule", "add", "fwmark", fmt.Sprintf("%d", client.Fwmark),
		"lookup", fmt.Sprintf("%d", client.Table))
	if err != nil {
		log.Printf("add rule fwmark %d: %v", client.Fwmark, err)
	}

	return nil
}

func (s *VPNService) DisconnectClient(ctx context.Context, name string) error {
	idx, client := s.findClient(name)
	if client == nil {
		return fmt.Errorf("tunnel %q not found", name)
	}

	iface := fmt.Sprintf("wg%d", idx)

	netutil.Run(ctx, "ip", "rule", "del", "fwmark", fmt.Sprintf("%d", client.Fwmark))
	netutil.Run(ctx, "ip", "route", "del", "default", "dev", iface, "table", fmt.Sprintf("%d", client.Table))

	_, err := netutil.Run(ctx, "wg-quick", "down", iface)
	return err
}

func (s *VPNService) GenerateKeypair(ctx context.Context) (privateKey, publicKey string, err error) {
	privOut, err := netutil.RunSimple(ctx, "wg", "genkey")
	if err != nil {
		return "", "", fmt.Errorf("genkey: %w", err)
	}
	privateKey = strings.TrimSpace(privOut)

	pubOut, err := netutil.RunSimple(ctx, "bash", "-c", fmt.Sprintf("echo '%s' | wg pubkey", privateKey))
	if err != nil {
		return "", "", fmt.Errorf("pubkey: %w", err)
	}
	publicKey = strings.TrimSpace(pubOut)

	return privateKey, publicKey, nil
}

func (s *VPNService) GeneratePresharedKey(ctx context.Context) (string, error) {
	out, err := netutil.RunSimple(ctx, "wg", "genpsk")
	if err != nil {
		return "", fmt.Errorf("genpsk: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// --- Server ---

type WGServerStatus struct {
	Enabled    bool
	Active     bool
	ListenPort int
	PublicKey  string
	PeerCount int
	Peers     []WGPeerStatus
}

type WGPeerStatus struct {
	Name         string
	PublicKey    string
	AllowedIPs   string
	Handshake    string
	Transfer     string
	Online       bool
}

func (s *VPNService) ServerStatus(ctx context.Context) (*WGServerStatus, error) {
	status := &WGServerStatus{
		Enabled:    s.cfg.VPN.Server.Enabled,
		ListenPort: s.cfg.VPN.Server.ListenPort,
	}

	out, err := netutil.RunSimple(ctx, "wg", "show", "wgs0")
	if err == nil && strings.Contains(out, "public key") {
		status.Active = true
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "public key:") {
				status.PublicKey = strings.TrimPrefix(line, "public key: ")
			}
		}
	}

	for _, peer := range s.cfg.VPN.Server.Peers {
		ps := WGPeerStatus{
			Name:       peer.Name,
			PublicKey:  peer.PublicKey,
			AllowedIPs: peer.AllowedIPs,
		}
		status.Peers = append(status.Peers, ps)
	}
	status.PeerCount = len(status.Peers)

	return status, nil
}

func (s *VPNService) ServerUp(ctx context.Context) error {
	confPath := "/etc/wireguard/wgs0.conf"
	if err := s.renderServerConfig(confPath); err != nil {
		return err
	}
	_, err := netutil.Run(ctx, "wg-quick", "up", "wgs0")
	return err
}

func (s *VPNService) ServerDown(ctx context.Context) error {
	_, err := netutil.Run(ctx, "wg-quick", "down", "wgs0")
	return err
}

func (s *VPNService) AddPeer(ctx context.Context, name string) (*config.WGServerPeer, string, error) {
	privKey, pubKey, err := s.GenerateKeypair(ctx)
	if err != nil {
		return nil, "", err
	}

	psk, _ := s.GeneratePresharedKey(ctx)

	nextIP := fmt.Sprintf("10.10.11.%d/32", len(s.cfg.VPN.Server.Peers)+2)

	peer := config.WGServerPeer{
		Name:         name,
		PublicKey:    pubKey,
		PresharedKey: psk,
		AllowedIPs:   nextIP,
		Keepalive:    25,
	}

	s.mu.Lock()
	s.cfg.VPN.Server.Peers = append(s.cfg.VPN.Server.Peers, peer)
	s.mu.Unlock()

	return &peer, privKey, nil
}

func (s *VPNService) RemovePeer(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, p := range s.cfg.VPN.Server.Peers {
		if p.Name == name {
			s.cfg.VPN.Server.Peers = append(s.cfg.VPN.Server.Peers[:i], s.cfg.VPN.Server.Peers[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("peer %q not found", name)
}

func (s *VPNService) GeneratePeerConfig(peer *config.WGServerPeer, peerPrivKey string) string {
	server := s.cfg.VPN.Server

	var sb strings.Builder
	fmt.Fprintf(&sb, "[Interface]\n")
	fmt.Fprintf(&sb, "PrivateKey = %s\n", peerPrivKey)
	fmt.Fprintf(&sb, "Address = %s\n", peer.AllowedIPs)
	fmt.Fprintf(&sb, "DNS = %s\n", server.DNS)
	if server.MTU > 0 {
		fmt.Fprintf(&sb, "MTU = %d\n", server.MTU)
	}
	fmt.Fprintf(&sb, "\n[Peer]\n")
	fmt.Fprintf(&sb, "PublicKey = %s\n", server.PublicKey)
	if peer.PresharedKey != "" {
		fmt.Fprintf(&sb, "PresharedKey = %s\n", peer.PresharedKey)
	}
	fmt.Fprintf(&sb, "Endpoint = <YOUR_PUBLIC_IP>:%d\n", server.ListenPort)
	fmt.Fprintf(&sb, "AllowedIPs = 0.0.0.0/0, ::/0\n")
	if peer.Keepalive > 0 {
		fmt.Fprintf(&sb, "PersistentKeepalive = %d\n", peer.Keepalive)
	}

	return sb.String()
}

func (s *VPNService) findClient(name string) (int, *config.WGClientTunnel) {
	for i := range s.cfg.VPN.Clients {
		if s.cfg.VPN.Clients[i].Name == name {
			return i, &s.cfg.VPN.Clients[i]
		}
	}
	return -1, nil
}

func (s *VPNService) renderClientConfig(client *config.WGClientTunnel, path string) error {
	tmpl, err := template.ParseFiles("configs/sysconf/wireguard-client.conf.tmpl")
	if err != nil {
		return fmt.Errorf("parse wg client template: %w", err)
	}

	os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	os.Chmod(path, 0o600)

	return tmpl.Execute(f, client)
}

func (s *VPNService) renderServerConfig(path string) error {
	tmpl, err := template.ParseFiles("configs/sysconf/wireguard-server.conf.tmpl")
	if err != nil {
		return fmt.Errorf("parse wg server template: %w", err)
	}

	os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	os.Chmod(path, 0o600)

	data := struct {
		config.WGServerConfig
		Peers []config.WGServerPeer
	}{
		WGServerConfig: s.cfg.VPN.Server,
		Peers:          s.cfg.VPN.Server.Peers,
	}

	return tmpl.Execute(f, data)
}
