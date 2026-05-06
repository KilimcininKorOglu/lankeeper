package services

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type VPNService struct {
	cfg *config.Config
	mu  sync.RWMutex
	// running tracks whether wgs0 has been brought up by this
	// process. Guarded by mu. ServerUp/ServerDown short-circuit when
	// the desired state already holds so a double-click in the UI
	// (or two browser tabs) cannot drive `wg-quick up/down` in
	// parallel and leave the kernel interface half-configured.
	running bool
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

	// Best-effort: missing rule/route is fine — this is teardown.
	_, _ = netutil.Run(ctx, "ip", "rule", "del", "fwmark", fmt.Sprintf("%d", client.Fwmark))
	_, _ = netutil.Run(ctx, "ip", "route", "del", "default", "dev", iface, "table", fmt.Sprintf("%d", client.Table))

	_, err := netutil.Run(ctx, "wg-quick", "down", iface)
	return err
}

func (s *VPNService) GenerateKeypair(ctx context.Context) (privateKey, publicKey string, err error) {
	privOut, err := netutil.RunSimple(ctx, "wg", "genkey")
	if err != nil {
		return "", "", fmt.Errorf("genkey: %w", err)
	}
	privateKey = strings.TrimSpace(privOut)

	pubOut, err := netutil.RunWithStdin(ctx, privateKey+"\n", "wg", "pubkey")
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
	Name          string
	PublicKey     string
	AllowedIPs    string
	Handshake     string
	Transfer      string
	Online        bool
	IsSiteToSite  bool
	RemoteSubnets []string
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
			Name:          peer.Name,
			PublicKey:     peer.PublicKey,
			AllowedIPs:   peer.AllowedIPs,
			IsSiteToSite:  peer.IsSiteToSite,
			RemoteSubnets: peer.RemoteSubnets,
		}
		status.Peers = append(status.Peers, ps)
	}
	status.PeerCount = len(status.Peers)

	return status, nil
}

// ErrVPNAlreadyRunning is returned when ServerUp is called while the
// wgs0 interface is already up under this process, and the analogous
// "already stopped" condition for ServerDown. Callers can choose to
// surface a UI message or treat as a no-op.
var (
	ErrVPNAlreadyRunning = fmt.Errorf("wireguard server already running")
	ErrVPNAlreadyStopped = fmt.Errorf("wireguard server already stopped")
)

func (s *VPNService) ServerUp(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrVPNAlreadyRunning
	}
	if err := s.renderServerConfig("/etc/wireguard/wgs0.conf"); err != nil {
		return err
	}
	if _, err := netutil.Run(ctx, "wg-quick", "up", "wgs0"); err != nil {
		return err
	}
	s.running = true
	return nil
}

func (s *VPNService) ServerDown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return ErrVPNAlreadyStopped
	}
	if _, err := netutil.Run(ctx, "wg-quick", "down", "wgs0"); err != nil {
		return err
	}
	s.running = false
	return nil
}

// RenderServerConfig writes /etc/wireguard/wgs0.conf without bringing the
// interface up. Suitable for install-time invocation by `render-configs`.
func (s *VPNService) RenderServerConfig(ctx context.Context) error {
	return s.renderServerConfig("/etc/wireguard/wgs0.conf")
}

// RenderAllClientConfigs writes /etc/wireguard/wgN.conf for every client
// tunnel in the config without bringing them up. Suitable for install-time.
func (s *VPNService) RenderAllClientConfigs(ctx context.Context) error {
	for idx := range s.cfg.VPN.Clients {
		client := &s.cfg.VPN.Clients[idx]
		iface := fmt.Sprintf("wg%d", idx)
		confPath := filepath.Join("/etc/wireguard", iface+".conf")
		if err := s.renderClientConfig(client, confPath); err != nil {
			return fmt.Errorf("render client %s: %w", client.Name, err)
		}
	}
	return nil
}

func (s *VPNService) AddPeer(ctx context.Context, name string, siteToSite bool, remoteSubnets []string, endpoint string) (*config.WGServerPeer, string, error) {
	privKey, pubKey, err := s.GenerateKeypair(ctx)
	if err != nil {
		return nil, "", err
	}

	psk, _ := s.GeneratePresharedKey(ctx)

	nextIP := fmt.Sprintf("10.10.11.%d/32", len(s.cfg.VPN.Server.Peers)+2)

	allowedIPs := nextIP
	if siteToSite && len(remoteSubnets) > 0 {
		allowedIPs = nextIP + ", " + strings.Join(remoteSubnets, ", ")
	}

	peer := config.WGServerPeer{
		Name:          name,
		PublicKey:     pubKey,
		PresharedKey:  psk,
		AllowedIPs:    allowedIPs,
		Keepalive:     25,
		Endpoint:      endpoint,
		RemoteSubnets: remoteSubnets,
		IsSiteToSite:  siteToSite,
	}

	s.mu.Lock()
	s.cfg.VPN.Server.Peers = append(s.cfg.VPN.Server.Peers, peer)
	s.mu.Unlock()

	if err := s.persist(); err != nil {
		return nil, "", fmt.Errorf("persist: %w", err)
	}
	return &peer, privKey, nil
}

func (s *VPNService) persist() error {
	return s.cfg.SaveToFile()
}

func (s *VPNService) RemovePeer(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, p := range s.cfg.VPN.Server.Peers {
		if p.Name == name {
			s.cfg.VPN.Server.Peers = append(s.cfg.VPN.Server.Peers[:i], s.cfg.VPN.Server.Peers[i+1:]...)
			return s.persist()
		}
	}
	return fmt.Errorf("peer %q not found", name)
}

func (s *VPNService) GeneratePeerConfig(peer *config.WGServerPeer, peerPrivKey string) string {
	server := s.cfg.VPN.Server

	peerTunnelIP := peer.AllowedIPs
	if idx := strings.Index(peerTunnelIP, ","); idx != -1 {
		peerTunnelIP = strings.TrimSpace(peerTunnelIP[:idx])
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[Interface]\n")
	fmt.Fprintf(&sb, "PrivateKey = %s\n", peerPrivKey)
	fmt.Fprintf(&sb, "Address = %s\n", peerTunnelIP)
	if !peer.IsSiteToSite {
		fmt.Fprintf(&sb, "DNS = %s\n", server.DNS)
	}
	if server.MTU > 0 {
		fmt.Fprintf(&sb, "MTU = %d\n", server.MTU)
	}
	fmt.Fprintf(&sb, "\n[Peer]\n")
	fmt.Fprintf(&sb, "PublicKey = %s\n", server.PublicKey)
	if peer.PresharedKey != "" {
		fmt.Fprintf(&sb, "PresharedKey = %s\n", peer.PresharedKey)
	}
	wgEndpoint := server.PublicEndpoint
	if wgEndpoint == "" {
		wgEndpoint = "<YOUR_PUBLIC_IP>"
	}
	fmt.Fprintf(&sb, "Endpoint = %s:%d\n", wgEndpoint, server.ListenPort)

	if peer.IsSiteToSite {
		var localSubnets []string
		for _, iface := range s.cfg.Interfaces {
			if iface.Role == "lan" && iface.Address != "" {
				localSubnets = append(localSubnets, s.addressToSubnet(iface.Address))
			}
		}
		if addr := server.Address; addr != "" {
			localSubnets = append(localSubnets, s.addressToSubnet(addr))
		}
		fmt.Fprintf(&sb, "AllowedIPs = %s\n", strings.Join(localSubnets, ", "))
	} else {
		fmt.Fprintf(&sb, "AllowedIPs = 0.0.0.0/0, ::/0\n")
	}

	if peer.Keepalive > 0 {
		fmt.Fprintf(&sb, "PersistentKeepalive = %d\n", peer.Keepalive)
	}

	return sb.String()
}

func (s *VPNService) addressToSubnet(addr string) string {
	if idx := strings.LastIndex(addr, "."); idx != -1 {
		slashIdx := strings.Index(addr, "/")
		mask := "/24"
		if slashIdx != -1 {
			mask = addr[slashIdx:]
		}
		return addr[:idx] + ".0" + mask
	}
	return addr
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

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, client); err != nil {
		return fmt.Errorf("render wg client config: %w", err)
	}

	return netutil.WriteFile(path, buf.Bytes(), 0o600)
}

func (s *VPNService) renderServerConfig(path string) error {
	tmpl, err := template.ParseFiles("configs/sysconf/wireguard-server.conf.tmpl")
	if err != nil {
		return fmt.Errorf("parse wg server template: %w", err)
	}

	data := struct {
		config.WGServerConfig
		Peers []config.WGServerPeer
	}{
		WGServerConfig: s.cfg.VPN.Server,
		Peers:          s.cfg.VPN.Server.Peers,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render wg server config: %w", err)
	}

	return netutil.WriteFile(path, buf.Bytes(), 0o600)
}
