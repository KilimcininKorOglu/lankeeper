package services

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
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
			for line := range strings.SplitSeq(out, "\n") {
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
	PeerCount  int
	Peers      []WGPeerStatus
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
		for line := range strings.SplitSeq(out, "\n") {
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
			AllowedIPs:    peer.AllowedIPs,
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
	if err := s.ensureServerKeypairLocked(ctx); err != nil {
		return err
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
	if err := s.EnsureServerKeypair(ctx); err != nil {
		return err
	}
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
	// The same overlap guard the invite wizard applies. Both forms write
	// into the same peer list and the same AllowedIPs mechanism, and the
	// rendered server config re-validates nothing, so a peer that claims
	// the LAN subnet here is authoritative for LAN traffic in WireGuard's
	// routing table. Checked before the keypair so a rejected request
	// costs no privileged commands.
	if err := s.checkRemoteSubnets(remoteSubnets); err != nil {
		return nil, "", err
	}
	if endpoint != "" {
		if err := ValidatePeerEndpoint(endpoint); err != nil {
			return nil, "", err
		}
	}

	privKey, pubKey, err := s.GenerateKeypair(ctx)
	if err != nil {
		return nil, "", err
	}

	psk, _ := s.GeneratePresharedKey(ctx)

	// Allocate, append and persist under one lock, matching RemovePeer.
	// nextTunnelIP derives the address from the peers actually present,
	// so splitting allocation from the append would let a concurrent
	// AddPeer read the same free slot and hand the same /32 to both.
	// Persisting inside the same section keeps the marshal from reading
	// the peer slice while another caller is appending to it.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Inside the lock so two concurrent adds cannot both pass the check
	// and then both append.
	if s.peerNameTakenLocked(name) {
		return nil, "", fmt.Errorf("%w: %s", ErrPeerNameInUse, name)
	}

	nextIP, err := s.nextTunnelIP()
	if err != nil {
		return nil, "", err
	}

	allowedIPs := nextIP
	if siteToSite && len(remoteSubnets) > 0 {
		allowedIPs = nextIP + ", " + strings.Join(remoteSubnets, ", ")
	}

	peer := config.WGServerPeer{
		Name:      name,
		PublicKey: pubKey,
		// Kept so the config can be handed over again later. The
		// caller still receives it directly for the one-shot download,
		// so this changes what survives, not what happens now.
		PrivateKey:    privKey,
		PresharedKey:  psk,
		AllowedIPs:    allowedIPs,
		Keepalive:     25,
		Endpoint:      endpoint,
		RemoteSubnets: remoteSubnets,
		IsSiteToSite:  siteToSite,
	}

	s.cfg.VPN.Server.Peers = append(s.cfg.VPN.Server.Peers, peer)

	if err := s.persist(); err != nil {
		return nil, "", fmt.Errorf("persist: %w", err)
	}
	return &peer, privKey, nil
}

// EnsureServerKeypair generates and persists the wgs0 key pair when the
// config carries none, and derives a missing public key from a present
// private key. An existing private key is never replaced, because every
// peer and every site-to-site link is bound to its public key.
func (s *VPNService) EnsureServerKeypair(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureServerKeypairLocked(ctx)
}

func (s *VPNService) ensureServerKeypairLocked(ctx context.Context) error {
	srv := &s.cfg.VPN.Server
	if srv.PrivateKey != "" && srv.PublicKey != "" {
		return nil
	}
	if srv.PrivateKey == "" {
		priv, pub, err := s.GenerateKeypair(ctx)
		if err != nil {
			return fmt.Errorf("generate server key pair: %w", err)
		}
		srv.PrivateKey, srv.PublicKey = priv, pub
	} else {
		pub, err := netutil.RunWithStdin(ctx, srv.PrivateKey+"\n", "wg", "pubkey")
		if err != nil {
			return fmt.Errorf("derive server public key: %w", err)
		}
		srv.PublicKey = strings.TrimSpace(pub)
	}
	if srv.PrivateKey == "" || srv.PublicKey == "" {
		return fmt.Errorf("wg returned an empty server key")
	}
	if err := s.persist(); err != nil {
		return fmt.Errorf("persist server key pair: %w", err)
	}
	return nil
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

var (
	ErrPeerNotFound = errors.New("peer not found")

	// ErrPeerKeyUnavailable separates "this peer predates key storage,
	// or its key could not be decrypted" from "no such peer". The two
	// need different answers: one is a missing resource, the other is a
	// peer that works but can never be re-issued, and the operator can
	// only act on the second by replacing it.
	ErrPeerKeyUnavailable = errors.New("peer private key is not stored, so its config cannot be re-issued")
)

// PeerConfig rebuilds a peer's client configuration from stored state.
func (s *VPNService) PeerConfig(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.cfg.VPN.Server.Peers {
		peer := &s.cfg.VPN.Server.Peers[i]
		if peer.Name != name {
			continue
		}
		if peer.PrivateKey == "" {
			return "", fmt.Errorf("%w: %s", ErrPeerKeyUnavailable, name)
		}
		return s.GeneratePeerConfig(peer, peer.PrivateKey), nil
	}
	return "", fmt.Errorf("%w: %s", ErrPeerNotFound, name)
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
	fmt.Fprintf(&sb, "Endpoint = %s:%d\n", cmp.Or(server.PublicEndpoint, "<YOUR_PUBLIC_IP>"), server.ListenPort)

	fmt.Fprintf(&sb, "AllowedIPs = %s\n", s.peerAllowedIPs(peer))

	if peer.Keepalive > 0 {
		fmt.Fprintf(&sb, "PersistentKeepalive = %d\n", peer.Keepalive)
	}

	return sb.String()
}

// peerAllowedIPs is what the peer routes into the tunnel: everything for
// a road-warrior peer, and the LAN and tunnel subnets for a
// site-to-site peer. That peer is a non-LANKeeper device with its own
// tunnel address in the server subnet, so unlike a wizard link it
// routes the tunnel subnet as well.
func (s *VPNService) peerAllowedIPs(peer *config.WGServerPeer) string {
	if !peer.IsSiteToSite {
		return "0.0.0.0/0, ::/0"
	}
	return strings.Join(s.reservedSubnets(), ", ")
}

// addressToSubnet turns an interface address such as 10.20.30.1/16 into
// its network, 10.20.0.0/16. An IPv4 address without a mask is taken as
// a /24. A value that does not parse is returned unchanged.
func (s *VPNService) addressToSubnet(addr string) string {
	addr = strings.TrimSpace(addr)
	if !strings.Contains(addr, "/") && strings.Contains(addr, ".") {
		addr += "/24"
	}
	_, ipNet, err := net.ParseCIDR(addr)
	if err != nil {
		return addr
	}
	return ipNet.String()
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
