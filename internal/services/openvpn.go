package services

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

type OpenVPNService struct {
	cfg *config.Config
	mu  sync.RWMutex
}

func NewOpenVPNService(cfg *config.Config) *OpenVPNService {
	return &OpenVPNService{cfg: cfg}
}

type OVPNServerStatus struct {
	Enabled      bool
	Active       bool
	PKIReady     bool
	Port         int
	Protocol     string
	ClientCount  int
}

func (s *OpenVPNService) ServerStatus(ctx context.Context) (*OVPNServerStatus, error) {
	status := &OVPNServerStatus{
		Enabled:  s.cfg.OpenVPN.Server.Enabled,
		Port:     s.cfg.OpenVPN.Server.Port,
		Protocol: s.cfg.OpenVPN.Server.Protocol,
	}

	if _, err := os.Stat("/etc/openvpn/pki/ca.crt"); err == nil {
		status.PKIReady = true
	}

	_, err := netutil.Run(ctx, "pgrep", "-x", "openvpn")
	status.Active = err == nil

	status.ClientCount = len(s.cfg.OpenVPN.Server.Clients)

	return status, nil
}

func (s *OpenVPNService) InitPKI(ctx context.Context) error {
	pkiDir := "/etc/openvpn/pki"
	easyrsa := "/usr/share/easy-rsa/easyrsa"
	env := []string{"EASYRSA_PKI=" + pkiDir}

	netutil.MkdirAll(pkiDir, 0o700)

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "init-pki"); err != nil {
		return fmt.Errorf("init-pki: %w", err)
	}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "build-ca", "nopass"); err != nil {
		return fmt.Errorf("build-ca: %w", err)
	}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "gen-req", "server", "nopass"); err != nil {
		return fmt.Errorf("gen-req server: %w", err)
	}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "sign-req", "server", "server"); err != nil {
		return fmt.Errorf("sign-req server: %w", err)
	}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "gen-dh"); err != nil {
		return fmt.Errorf("gen-dh: %w", err)
	}

	if _, err := netutil.Run(ctx, "openvpn", "--genkey", "secret", pkiDir+"/ta.key"); err != nil {
		return fmt.Errorf("gen tls-auth key: %w", err)
	}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "gen-crl"); err != nil {
		return fmt.Errorf("gen-crl: %w", err)
	}

	log.Println("OpenVPN PKI initialized")
	return nil
}

func (s *OpenVPNService) AddClient(ctx context.Context, name string, siteToSite bool, remoteSubnets []string, fixedIP string) error {
	easyrsa := "/usr/share/easy-rsa/easyrsa"
	env := []string{"EASYRSA_PKI=/etc/openvpn/pki"}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "gen-req", name, "nopass"); err != nil {
		return fmt.Errorf("gen-req %s: %w", name, err)
	}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "sign-req", "client", name); err != nil {
		return fmt.Errorf("sign-req %s: %w", name, err)
	}

	entry := config.OVPNClientEntry{
		Name:          name,
		CommonName:    name,
		Enabled:       true,
		IsSiteToSite:  siteToSite,
		RemoteSubnets: remoteSubnets,
		FixedIP:       fixedIP,
	}

	s.mu.Lock()
	s.cfg.OpenVPN.Server.Clients = append(s.cfg.OpenVPN.Server.Clients, entry)
	s.mu.Unlock()

	if err := s.writeCCD(entry); err != nil {
		log.Printf("write CCD for %s: %v", name, err)
	}

	s.persist()
	log.Printf("OpenVPN client %q added (s2s=%v)", name, siteToSite)
	return nil
}

func (s *OpenVPNService) persist() error {
	return s.cfg.SaveToFile()
}

func (s *OpenVPNService) RevokeClient(ctx context.Context, name string) error {
	easyrsa := "/usr/share/easy-rsa/easyrsa"
	env := []string{"EASYRSA_PKI=/etc/openvpn/pki"}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "revoke", name); err != nil {
		return fmt.Errorf("revoke %s: %w", name, err)
	}

	if _, err := netutil.RunWithEnv(ctx, env, easyrsa, "gen-crl"); err != nil {
		return fmt.Errorf("gen-crl: %w", err)
	}

	s.mu.Lock()
	for i := range s.cfg.OpenVPN.Server.Clients {
		if s.cfg.OpenVPN.Server.Clients[i].Name == name {
			s.cfg.OpenVPN.Server.Clients[i].Enabled = false
			break
		}
	}
	s.mu.Unlock()

	s.persist()
	log.Printf("OpenVPN client %q revoked", name)
	return nil
}

func (s *OpenVPNService) ListServerClients() []config.OVPNClientEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]config.OVPNClientEntry, len(s.cfg.OpenVPN.Server.Clients))
	copy(result, s.cfg.OpenVPN.Server.Clients)
	return result
}

func (s *OpenVPNService) GenerateClientOVPN(name string) (string, error) {
	pkiDir := "/etc/openvpn/pki"

	ca, err := os.ReadFile(pkiDir + "/ca.crt")
	if err != nil {
		return "", fmt.Errorf("read CA: %w", err)
	}

	cert, err := os.ReadFile(fmt.Sprintf("%s/issued/%s.crt", pkiDir, name))
	if err != nil {
		return "", fmt.Errorf("read cert: %w", err)
	}

	key, err := os.ReadFile(fmt.Sprintf("%s/private/%s.key", pkiDir, name))
	if err != nil {
		return "", fmt.Errorf("read key: %w", err)
	}

	ta, err := os.ReadFile(pkiDir + "/ta.key")
	if err != nil {
		return "", fmt.Errorf("read ta.key: %w", err)
	}

	srv := s.cfg.OpenVPN.Server

	endpoint := srv.PublicEndpoint
	if endpoint == "" {
		endpoint = "<YOUR_PUBLIC_IP>"
	}

	var clientEntry *config.OVPNClientEntry
	for i := range srv.Clients {
		if srv.Clients[i].Name == name || srv.Clients[i].CommonName == name {
			clientEntry = &srv.Clients[i]
			break
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "client\n")
	fmt.Fprintf(&sb, "dev tun\n")
	fmt.Fprintf(&sb, "proto %s\n", srv.Protocol)
	fmt.Fprintf(&sb, "remote %s %d\n", endpoint, srv.Port)
	fmt.Fprintf(&sb, "resolv-retry infinite\n")
	fmt.Fprintf(&sb, "nobind\n")
	fmt.Fprintf(&sb, "persist-key\n")
	fmt.Fprintf(&sb, "persist-tun\n")
	fmt.Fprintf(&sb, "cipher %s\n", srv.Cipher)
	fmt.Fprintf(&sb, "auth %s\n", srv.Auth)
	fmt.Fprintf(&sb, "key-direction 1\n")
	fmt.Fprintf(&sb, "verb 3\n")

	if clientEntry != nil && clientEntry.IsSiteToSite {
		fmt.Fprintf(&sb, "route-nopull\n")
		for _, iface := range s.cfg.Interfaces {
			if iface.Role == "lan" && iface.Address != "" {
				subnetIP := subnetFromCIDR(iface.Address)
				_, mask := cidrToIPMask(iface.Address)
				if subnetIP != "" {
					fmt.Fprintf(&sb, "route %s %s\n", subnetIP, mask)
				}
			}
		}
		ovpnSubnetIP := subnetFromCIDR(srv.Subnet)
		_, ovpnMask := cidrToIPMask(srv.Subnet)
		if ovpnSubnetIP != "" {
			fmt.Fprintf(&sb, "route %s %s\n", ovpnSubnetIP, ovpnMask)
		}
	}

	fmt.Fprintf(&sb, "\n<ca>\n%s</ca>\n\n", ca)
	fmt.Fprintf(&sb, "<cert>\n%s</cert>\n\n", cert)
	fmt.Fprintf(&sb, "<key>\n%s</key>\n\n", key)
	fmt.Fprintf(&sb, "<tls-auth>\n%s</tls-auth>\n", ta)

	return sb.String(), nil
}

type ovpnServerTemplateData struct {
	config.OVPNServerConfig
	SubnetIP        string
	SubnetMask      string
	SiteToSiteRoutes []ovpnRouteEntry
}

type ovpnRouteEntry struct {
	SubnetIP   string
	SubnetMask string
}

func (s *OpenVPNService) RenderServerConfig() error {
	srv := s.cfg.OpenVPN.Server

	tmpl, err := template.ParseFiles("configs/sysconf/openvpn-server.conf.tmpl")
	if err != nil {
		return fmt.Errorf("parse openvpn server template: %w", err)
	}

	subnetIP, subnetMask := cidrToIPMask(srv.Subnet)

	data := ovpnServerTemplateData{
		OVPNServerConfig: srv,
		SubnetIP:         subnetIP,
		SubnetMask:       subnetMask,
	}

	for _, client := range srv.Clients {
		if client.IsSiteToSite && client.Enabled {
			for _, subnet := range client.RemoteSubnets {
				ip, mask := cidrToIPMask(subnet)
				if ip != "" {
					data.SiteToSiteRoutes = append(data.SiteToSiteRoutes, ovpnRouteEntry{
						SubnetIP:   ip,
						SubnetMask: mask,
					})
				}
			}
		}
	}

	netutil.MkdirAll("/etc/openvpn", 0o755)
	netutil.MkdirAll("/etc/openvpn/ccd", 0o755)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render server.conf: %w", err)
	}

	if err := netutil.WriteFile("/etc/openvpn/server.conf", buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write server.conf: %w", err)
	}

	for _, client := range srv.Clients {
		if client.Enabled {
			if err := s.writeCCD(client); err != nil {
				log.Printf("write CCD for %s: %v", client.Name, err)
			}
		}
	}

	return nil
}

func (s *OpenVPNService) writeCCD(entry config.OVPNClientEntry) error {
	ccdDir := "/etc/openvpn/ccd"
	netutil.MkdirAll(ccdDir, 0o755)

	var sb strings.Builder

	if entry.FixedIP != "" {
		ip, mask := cidrToIPMask(entry.FixedIP + "/24")
		if ip != "" {
			fmt.Fprintf(&sb, "ifconfig-push %s %s\n", entry.FixedIP, mask)
			_ = ip
		}
	}

	if entry.IsSiteToSite {
		fmt.Fprintf(&sb, "push-reset\n")
		for _, iface := range s.cfg.Interfaces {
			if iface.Role == "lan" && iface.Address != "" {
				ip, mask := cidrToIPMask(iface.Address)
				if ip != "" {
					fmt.Fprintf(&sb, "push \"route %s %s\"\n", subnetFromCIDR(iface.Address), mask)
				}
			}
		}
		for _, subnet := range entry.RemoteSubnets {
			ip, mask := cidrToIPMask(subnet)
			if ip != "" {
				fmt.Fprintf(&sb, "iroute %s %s\n", ip, mask)
			}
		}
	}

	cn := entry.CommonName
	if cn == "" {
		cn = entry.Name
	}

	return netutil.WriteFile(filepath.Join(ccdDir, cn), []byte(sb.String()), 0o644)
}

func cidrToIPMask(cidr string) (string, string) {
	if !strings.Contains(cidr, "/") {
		return cidr, "255.255.255.0"
	}
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", ""
	}
	mask := net.IP(ipNet.Mask).String()
	return ip.String(), mask
}

func subnetFromCIDR(cidr string) string {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	return ipNet.IP.String()
}

func (s *OpenVPNService) ServerStart(ctx context.Context) error {
	if err := s.RenderServerConfig(); err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	_, err := netutil.Run(ctx, "systemctl", "start", "openvpn@server")
	return err
}

func (s *OpenVPNService) ServerStop(ctx context.Context) error {
	_, err := netutil.Run(ctx, "systemctl", "stop", "openvpn@server")
	return err
}

func (s *OpenVPNService) ImportClientConfig(name, ovpnContent string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cfg.OpenVPN.Clients = append(s.cfg.OpenVPN.Clients, config.OVPNClientConfig{
		Name:       name,
		ConfigFile: ovpnContent,
	})
	s.persist()
}

func (s *OpenVPNService) AddOutboundClient(client config.OVPNClientConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.OpenVPN.Clients = append(s.cfg.OpenVPN.Clients, client)
	s.persist()
}

func (s *OpenVPNService) ListOutboundClients() []config.OVPNClientConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]config.OVPNClientConfig, len(s.cfg.OpenVPN.Clients))
	copy(result, s.cfg.OpenVPN.Clients)
	return result
}

func (s *OpenVPNService) renderClientConfig(c config.OVPNClientConfig, confPath string) error {
	if c.ConfigFile != "" {
		return netutil.WriteFile(confPath, []byte(c.ConfigFile), 0o600)
	}

	tmpl, err := template.ParseFiles("configs/sysconf/openvpn-client.conf.tmpl")
	if err != nil {
		return fmt.Errorf("parse openvpn client template: %w", err)
	}

	if c.RemotePort == 0 {
		c.RemotePort = 1194
	}
	if c.Protocol == "" {
		c.Protocol = "udp"
	}
	if c.Cipher == "" {
		c.Cipher = "AES-256-GCM"
	}
	if c.Auth == "" {
		c.Auth = "SHA256"
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, c); err != nil {
		return fmt.Errorf("render client config: %w", err)
	}

	if err := netutil.WriteFile(confPath, buf.Bytes(), 0o600); err != nil {
		return err
	}

	if c.Username != "" && c.Password != "" {
		authPath := fmt.Sprintf("/etc/openvpn/client/%s-auth.txt", c.Name)
		authContent := fmt.Sprintf("%s\n%s\n", c.Username, c.Password)
		netutil.WriteFile(authPath, []byte(authContent), 0o600)
	}

	return nil
}

func (s *OpenVPNService) ConnectClient(ctx context.Context, name string) error {
	for _, c := range s.cfg.OpenVPN.Clients {
		if c.Name == name {
			confPath := fmt.Sprintf("/etc/openvpn/client/%s.conf", name)
			netutil.MkdirAll("/etc/openvpn/client", 0o700)

			if err := s.renderClientConfig(c, confPath); err != nil {
				return fmt.Errorf("render client config: %w", err)
			}

			_, err := netutil.Run(ctx, "openvpn", "--config", confPath, "--daemon",
				"--writepid", fmt.Sprintf("/var/run/openvpn-%s.pid", name))
			return err
		}
	}
	return fmt.Errorf("client %q not found", name)
}

func (s *OpenVPNService) DisconnectClient(ctx context.Context, name string) error {
	pidFile := fmt.Sprintf("/var/run/openvpn-%s.pid", name)
	pidData, err := os.ReadFile(pidFile)
	if err == nil {
		pid := strings.TrimSpace(string(pidData))
		netutil.Run(ctx, "kill", pid)
		os.Remove(pidFile)
	}
	log.Printf("OpenVPN client %q disconnected", name)
	return nil
}
