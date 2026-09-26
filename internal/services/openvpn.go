package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// ovpnClientNamePattern is the character allowlist for an OpenVPN
// client name. The same expression previously lived only in the
// handlers package and was applied by some handlers and not others,
// which is why it now sits beside the code that turns a name into a
// file path and a command argument.
var ovpnClientNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// maxOVPNClientNameLen matches the cap the handlers already applied.
const maxOVPNClientNameLen = 64

// ErrInvalidClientName rejects a name before it reaches a path or an
// argv entry.
var ErrInvalidClientName = errors.New("client name must be 1-64 characters of letters, digits, underscores and hyphens")

// ValidateOpenVPNClientName reports whether name is safe to interpolate
// into a PKI command argument or a pid/config file path.
func ValidateOpenVPNClientName(name string) error {
	if len(name) > maxOVPNClientNameLen || !ovpnClientNamePattern.MatchString(name) {
		return ErrInvalidClientName
	}
	return nil
}

type OpenVPNService struct {
	cfg *config.Config
	mu  sync.RWMutex
	// running tracks whether `openvpn@server` has been started by
	// this process. Guarded by mu. ServerStart/ServerStop
	// short-circuit when the desired state already holds so a
	// concurrent UI click cannot race two systemctl invocations.
	running bool
}

// ErrOpenVPNAlreadyRunning / ErrOpenVPNAlreadyStopped let handlers
// distinguish a benign idempotent retry from a genuine failure.
var (
	ErrOpenVPNAlreadyRunning = fmt.Errorf("openvpn server already running")
	ErrOpenVPNAlreadyStopped = fmt.Errorf("openvpn server already stopped")
)

func NewOpenVPNService(cfg *config.Config) *OpenVPNService {
	return &OpenVPNService{cfg: cfg}
}

type OVPNServerStatus struct {
	Enabled     bool
	Active      bool
	PKIReady    bool
	Port        int
	Protocol    string
	ClientCount int
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

// pkiSetupBudget covers the whole PKI bring-up. Key generation, and
// gen-dh in particular, can run for minutes on the low-power hardware
// this targets, and the agent applies a short default to any command
// whose caller expressed no budget. Without an explicit one here those
// steps are killed mid-generation.
const pkiSetupBudget = 10 * time.Minute

func (s *OpenVPNService) InitPKI(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pkiSetupBudget)
	defer cancel()

	pkiDir := "/etc/openvpn/pki"
	easyrsa := "/usr/share/easy-rsa/easyrsa"

	if err := netutil.MkdirAll(pkiDir, 0o700); err != nil {
		return fmt.Errorf("mkdir pki: %w", err)
	}

	if _, err := netutil.Run(ctx, easyrsa, "init-pki"); err != nil {
		return fmt.Errorf("init-pki: %w", err)
	}

	if _, err := netutil.Run(ctx, easyrsa, "build-ca", "nopass"); err != nil {
		return fmt.Errorf("build-ca: %w", err)
	}

	if _, err := netutil.Run(ctx, easyrsa, "gen-req", "server", "nopass"); err != nil {
		return fmt.Errorf("gen-req server: %w", err)
	}

	if _, err := netutil.Run(ctx, easyrsa, "sign-req", "server", "server"); err != nil {
		return fmt.Errorf("sign-req server: %w", err)
	}

	if _, err := netutil.Run(ctx, easyrsa, "gen-dh"); err != nil {
		return fmt.Errorf("gen-dh: %w", err)
	}

	if _, err := netutil.Run(ctx, "openvpn", "--genkey", "secret", pkiDir+"/ta.key"); err != nil {
		return fmt.Errorf("gen tls-auth key: %w", err)
	}

	if _, err := netutil.Run(ctx, easyrsa, "gen-crl"); err != nil {
		return fmt.Errorf("gen-crl: %w", err)
	}

	log.Println("OpenVPN PKI initialized")
	return nil
}

func (s *OpenVPNService) AddClient(ctx context.Context, name string, siteToSite bool, remoteSubnets []string, fixedIP string) error {
	if err := ValidateOpenVPNClientName(name); err != nil {
		return err
	}
	if err := s.validateFixedIP(fixedIP); err != nil {
		return err
	}

	easyrsa := "/usr/share/easy-rsa/easyrsa"

	if _, err := netutil.Run(ctx, easyrsa, "gen-req", name, "nopass"); err != nil {
		return fmt.Errorf("gen-req %s: %w", name, err)
	}

	if _, err := netutil.Run(ctx, easyrsa, "sign-req", "client", name); err != nil {
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

	if err := s.persist(); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	log.Printf("OpenVPN client %q added (s2s=%v)", name, siteToSite)
	return nil
}

// ErrInvalidFixedIP reports a fixed client address the server cannot
// push.
var ErrInvalidFixedIP = errors.New("fixed address must be a host address inside the OpenVPN subnet")

// validateFixedIP accepts "" or an IPv4 host address inside the server
// subnet other than its network, broadcast and server addresses. The
// client config skips any other value, so without this check the client
// was stored with an address it never received.
func (s *OpenVPNService) validateFixedIP(fixedIP string) error {
	if fixedIP == "" {
		return nil
	}
	ip := net.ParseIP(fixedIP).To4()
	_, subnet, err := net.ParseCIDR(s.cfg.OpenVPN.Server.Subnet)
	if ip == nil || err != nil || !subnet.Contains(ip) {
		return fmt.Errorf("%w: %s", ErrInvalidFixedIP, fixedIP)
	}
	host := binary.BigEndian.Uint32(ip) &^ binary.BigEndian.Uint32(subnet.Mask)
	hostMask := ^binary.BigEndian.Uint32(subnet.Mask)
	// host 1 is the server's own address under `server`.
	if host == 0 || host == 1 || host == hostMask {
		return fmt.Errorf("%w: %s", ErrInvalidFixedIP, fixedIP)
	}
	return nil
}

func (s *OpenVPNService) persist() error {
	return s.cfg.SaveToFile()
}

func (s *OpenVPNService) RevokeClient(ctx context.Context, name string) error {
	if err := ValidateOpenVPNClientName(name); err != nil {
		return err
	}

	easyrsa := "/usr/share/easy-rsa/easyrsa"

	if _, err := netutil.Run(ctx, easyrsa, "revoke", name); err != nil {
		return fmt.Errorf("revoke %s: %w", name, err)
	}

	if _, err := netutil.Run(ctx, easyrsa, "gen-crl"); err != nil {
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

	if err := s.persist(); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
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
	// The name indexes two PKI files below, so a traversing value would
	// read an arbitrary file and hand it back as a downloadable profile.
	if err := ValidateOpenVPNClientName(name); err != nil {
		return "", err
	}

	pki, err := readClientPKI(name)
	if err != nil {
		return "", err
	}

	srv := s.cfg.OpenVPN.Server

	endpoint := srv.PublicEndpoint
	if endpoint == "" {
		endpoint = "<YOUR_PUBLIC_IP>"
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
	if srv.Compression {
		// Must match the server template, or the two ends disagree on
		// whether data packets carry a compression header.
		fmt.Fprintf(&sb, "compress\n")
	}
	fmt.Fprintf(&sb, "key-direction 1\n")
	fmt.Fprintf(&sb, "verb 3\n")

	if entry := findServerClient(srv.Clients, name); entry != nil && entry.IsSiteToSite {
		fmt.Fprintf(&sb, "route-nopull\n")
		for _, subnet := range s.lanSubnets() {
			writeRoute(&sb, "route %s %s\n", subnet)
		}
		writeRoute(&sb, "route %s %s\n", srv.Subnet)
	}

	fmt.Fprintf(&sb, "\n<ca>\n%s</ca>\n\n", pki.ca)
	fmt.Fprintf(&sb, "<cert>\n%s</cert>\n\n", pki.cert)
	fmt.Fprintf(&sb, "<key>\n%s</key>\n\n", pki.key)
	fmt.Fprintf(&sb, "<tls-auth>\n%s</tls-auth>\n", pki.ta)

	return sb.String(), nil
}

// clientPKI is the key material embedded in a client profile.
type clientPKI struct {
	ca, cert, key, ta []byte
}

// readClientPKI reads the CA, the client's certificate and key, and the
// TLS auth key. name must already be validated: it indexes two files.
func readClientPKI(name string) (*clientPKI, error) {
	const pkiDir = "/etc/openvpn/pki"
	files := []struct {
		path, label string
	}{
		{pkiDir + "/ca.crt", "CA"},
		{fmt.Sprintf("%s/issued/%s.crt", pkiDir, name), "cert"},
		{fmt.Sprintf("%s/private/%s.key", pkiDir, name), "key"},
		{pkiDir + "/ta.key", "ta.key"},
	}
	contents := make([][]byte, len(files))
	for i, f := range files {
		// Through the agent: easy-rsa runs as root, so the private key
		// and its directory are root-only and this process cannot open
		// them directly.
		b, err := netutil.ReadFile(f.path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.label, err)
		}
		contents[i] = b
	}
	return &clientPKI{ca: contents[0], cert: contents[1], key: contents[2], ta: contents[3]}, nil
}

// findServerClient returns the server client whose name or common name
// is name, or nil.
func findServerClient(clients []config.OVPNClientEntry, name string) *config.OVPNClientEntry {
	for i := range clients {
		if clients[i].Name == name || clients[i].CommonName == name {
			return &clients[i]
		}
	}
	return nil
}

// lanSubnets lists the address of every LAN interface that has one.
func (s *OpenVPNService) lanSubnets() []string {
	var subnets []string
	for _, iface := range s.cfg.Interfaces {
		if iface.Role == "lan" && iface.Address != "" {
			subnets = append(subnets, iface.Address)
		}
	}
	return subnets
}

// writeRoute writes format with the network address and mask of cidr,
// or nothing when cidr does not parse.
func writeRoute(sb *strings.Builder, format, cidr string) {
	subnetIP := subnetFromCIDR(cidr)
	_, mask := cidrToIPMask(cidr)
	if subnetIP != "" {
		fmt.Fprintf(sb, format, subnetIP, mask)
	}
}

type ovpnServerTemplateData struct {
	config.OVPNServerConfig
	SubnetIP         string
	SubnetMask       string
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
		SiteToSiteRoutes: siteToSiteRoutes(srv.Clients),
	}

	if err := netutil.MkdirAll("/etc/openvpn", 0o755); err != nil {
		return fmt.Errorf("mkdir /etc/openvpn: %w", err)
	}
	if err := netutil.MkdirAll("/etc/openvpn/ccd", 0o755); err != nil {
		return fmt.Errorf("mkdir /etc/openvpn/ccd: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render server.conf: %w", err)
	}

	if err := netutil.WriteFile("/etc/openvpn/server.conf", buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write server.conf: %w", err)
	}

	s.writeEnabledCCDs(srv.Clients)
	return nil
}

// siteToSiteRoutes lists the remote subnets of every enabled
// site-to-site client that parse.
func siteToSiteRoutes(clients []config.OVPNClientEntry) []ovpnRouteEntry {
	var routes []ovpnRouteEntry
	for _, client := range clients {
		if !client.IsSiteToSite || !client.Enabled {
			continue
		}
		for _, subnet := range client.RemoteSubnets {
			if ip, mask := cidrToIPMask(subnet); ip != "" {
				routes = append(routes, ovpnRouteEntry{SubnetIP: ip, SubnetMask: mask})
			}
		}
	}
	return routes
}

// writeEnabledCCDs writes the client-config file of every enabled
// client. A failure is logged per client.
func (s *OpenVPNService) writeEnabledCCDs(clients []config.OVPNClientEntry) {
	for _, client := range clients {
		if !client.Enabled {
			continue
		}
		if err := s.writeCCD(client); err != nil {
			log.Printf("write CCD for %s: %v", client.Name, err)
		}
	}
}

func (s *OpenVPNService) writeCCD(entry config.OVPNClientEntry) error {
	ccdDir := "/etc/openvpn/ccd"
	if err := netutil.MkdirAll(ccdDir, 0o755); err != nil {
		return fmt.Errorf("mkdir ccd: %w", err)
	}

	cn := entry.CommonName
	if cn == "" {
		cn = entry.Name
	}

	return netutil.WriteFile(filepath.Join(ccdDir, cn), []byte(s.ccdContent(entry)), 0o644)
}

// ccdContent renders the client-config directives: the fixed address,
// and for a site-to-site client the pushed LAN routes and its own
// remote subnets.
//
// The server runs topology subnet, where ifconfig-push takes the
// client address and the netmask of the server subnet. Under OpenVPN's
// default net30 the second argument is the peer endpoint instead, so an
// address and a mask pushed there configured an unusable interface.
func (s *OpenVPNService) ccdContent(entry config.OVPNClientEntry) string {
	var sb strings.Builder

	if entry.FixedIP != "" {
		_, mask := cidrToIPMask(s.cfg.OpenVPN.Server.Subnet)
		if ip := net.ParseIP(entry.FixedIP).To4(); ip != nil && mask != "" {
			fmt.Fprintf(&sb, "ifconfig-push %s %s\n", ip, mask)
		}
	}
	if !entry.IsSiteToSite {
		return sb.String()
	}

	fmt.Fprintf(&sb, "push-reset\n")
	for _, addr := range s.lanSubnets() {
		if ip, mask := cidrToIPMask(addr); ip != "" {
			fmt.Fprintf(&sb, "push \"route %s %s\"\n", subnetFromCIDR(addr), mask)
		}
	}
	for _, subnet := range entry.RemoteSubnets {
		if ip, mask := cidrToIPMask(subnet); ip != "" {
			fmt.Fprintf(&sb, "iroute %s %s\n", ip, mask)
		}
	}
	return sb.String()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrOpenVPNAlreadyRunning
	}
	if err := s.RenderServerConfig(); err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	if _, err := netutil.Run(ctx, "systemctl", "start", "openvpn@server"); err != nil {
		return err
	}
	s.running = true
	return nil
}

func (s *OpenVPNService) ServerStop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return ErrOpenVPNAlreadyStopped
	}
	if _, err := netutil.Run(ctx, "systemctl", "stop", "openvpn@server"); err != nil {
		return err
	}
	s.running = false
	return nil
}

func (s *OpenVPNService) ImportClientConfig(name, ovpnContent string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cfg.OpenVPN.Clients = append(s.cfg.OpenVPN.Clients, config.OVPNClientConfig{
		Name:       name,
		ConfigFile: ovpnContent,
	})
	if err := s.persist(); err != nil {
		log.Printf("openvpn add inbound client: persist: %v", err)
	}
}

func (s *OpenVPNService) AddOutboundClient(client config.OVPNClientConfig) error {
	if err := ValidateOutboundClient(client); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.OpenVPN.Clients = append(s.cfg.OpenVPN.Clients, client)
	return s.persist()
}

func (s *OpenVPNService) ListOutboundClients() []config.OVPNClientConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]config.OVPNClientConfig, len(s.cfg.OpenVPN.Clients))
	copy(result, s.cfg.OpenVPN.Clients)
	return result
}

func (s *OpenVPNService) renderClientConfig(c config.OVPNClientConfig, confPath string) error {
	// Checked again here because a stored client may predate the check,
	// and this file is what the root agent hands to openvpn.
	if err := ValidateOutboundClient(c); err != nil {
		return err
	}
	if c.ConfigFile != "" {
		return netutil.WriteFile(confPath, []byte(c.ConfigFile), 0o600)
	}

	tmpl, err := template.ParseFiles("configs/sysconf/openvpn-client.conf.tmpl")
	if err != nil {
		return fmt.Errorf("parse openvpn client template: %w", err)
	}

	applyClientDefaults(&c)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, c); err != nil {
		return fmt.Errorf("render client config: %w", err)
	}

	if err := netutil.WriteFile(confPath, buf.Bytes(), 0o600); err != nil {
		return err
	}

	return writeClientAuth(c)
}

// applyClientDefaults fills the port, protocol, cipher and digest an
// outbound client left empty.
func applyClientDefaults(c *config.OVPNClientConfig) {
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
}

// writeClientAuth writes the credentials file an outbound client with a
// username and password reads, and does nothing otherwise.
func writeClientAuth(c config.OVPNClientConfig) error {
	if c.Username == "" || c.Password == "" {
		return nil
	}
	authPath := fmt.Sprintf("/etc/openvpn/client/%s-auth.txt", c.Name)
	authContent := fmt.Sprintf("%s\n%s\n", c.Username, c.Password)
	if err := netutil.WriteFile(authPath, []byte(authContent), 0o600); err != nil {
		return fmt.Errorf("write auth file: %w", err)
	}
	return nil
}

func (s *OpenVPNService) ConnectClient(ctx context.Context, name string) error {
	if err := ValidateOpenVPNClientName(name); err != nil {
		return err
	}

	for _, c := range s.cfg.OpenVPN.Clients {
		if c.Name == name {
			confPath := fmt.Sprintf("/etc/openvpn/client/%s.conf", name)
			if err := netutil.MkdirAll("/etc/openvpn/client", 0o700); err != nil {
				return fmt.Errorf("mkdir client dir: %w", err)
			}

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
	// The name becomes a path here, and whatever that path holds is
	// handed to kill through the root agent. Validate at this boundary
	// rather than trusting every caller to have done it.
	if err := ValidateOpenVPNClientName(name); err != nil {
		return err
	}

	pidFile := fmt.Sprintf("/var/run/openvpn-%s.pid", name)
	// pidFile is built from a client name matched against the
	// configured client list first.
	// #nosec G304
	pidData, err := os.ReadFile(pidFile)
	if err == nil {
		pid := strings.TrimSpace(string(pidData))
		if _, err := netutil.Run(ctx, "kill", pid); err != nil {
			log.Printf("openvpn disconnect %s: kill: %v", name, err)
		}
		if err := os.Remove(pidFile); err != nil {
			log.Printf("openvpn disconnect %s: remove pidfile: %v", name, err)
		}
	}
	log.Printf("OpenVPN client %q disconnected", name)
	return nil
}

// ovpnStatusPath is the file the shipped server config writes its client
// list to. Kept in step with configs/sysconf/openvpn-server.conf.tmpl.
const ovpnStatusPath = "/var/log/openvpn-status.log"

// ActiveSessions counts the clients currently connected to the OpenVPN
// server, read from the status file the daemon maintains.
//
// The shipped config sets no status-version, so the file is OpenVPN's
// default version 1: a "CLIENT LIST" section whose header row starts
// with "Common Name", one row per connected client, terminated by
// "ROUTING TABLE". The machine-readable versions prefix each row with
// "CLIENT_LIST,", which is accepted too so an operator who sets
// status-version by hand does not silently get zero.
//
// A missing file means the server has never run, which is zero sessions
// rather than an error.
func (s *OpenVPNService) ActiveSessions(ctx context.Context) int {
	data, err := netutil.ReadFile(ovpnStatusPath)
	if err != nil {
		return 0
	}
	return countOVPNSessions(string(data))
}

func countOVPNSessions(status string) int {
	var count int
	inClientList := false

	for line := range strings.SplitSeq(status, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "CLIENT_LIST,"):
			count++
			continue
		case strings.HasPrefix(line, "Common Name,"):
			inClientList = true
			continue
		case strings.HasPrefix(line, "ROUTING TABLE"),
			strings.HasPrefix(line, "GLOBAL STATS"),
			line == "END":
			inClientList = false
			continue
		}
		if inClientList && strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
