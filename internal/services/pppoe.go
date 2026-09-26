package services

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"unicode"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type PPPoEService struct {
	cfg       *config.Config
	mu        sync.RWMutex
	connected bool

	// Cross-service hooks invoked after a successful Connect / Disconnect.
	// Used by IPv6Service to (re)start the dhcp6c PD client whenever the
	// ppp0 interface is rebuilt. Hook errors are logged but never block
	// the PPPoE state transition itself.
	onConnect    func(ctx context.Context) error
	onDisconnect func(ctx context.Context) error
}

func NewPPPoEService(cfg *config.Config) *PPPoEService {
	return &PPPoEService{cfg: cfg}
}

// SetOnConnect registers a callback to run after every successful Connect.
func (s *PPPoEService) SetOnConnect(fn func(ctx context.Context) error) {
	s.mu.Lock()
	s.onConnect = fn
	s.mu.Unlock()
}

// SetOnDisconnect registers a callback to run after every Disconnect.
func (s *PPPoEService) SetOnDisconnect(fn func(ctx context.Context) error) {
	s.mu.Lock()
	s.onDisconnect = fn
	s.mu.Unlock()
}

type PPPoEStatus struct {
	Connected bool
	Interface string
	LocalIP   string
	RemoteIP  string
	LocalIPv6 string
	Uptime    string
	PID       int
}

func (s *PPPoEService) Status(ctx context.Context) (*PPPoEStatus, error) {
	pid, err := s.readPID()
	if err != nil || pid == 0 {
		return &PPPoEStatus{Connected: false}, nil
	}

	if !processExists(pid) {
		return &PPPoEStatus{Connected: false}, nil
	}

	status := &PPPoEStatus{
		Connected: true,
		Interface: "ppp0",
		PID:       pid,
	}

	addrs, err := netutil.GetInterfaceAddresses("ppp0")
	if err == nil {
		status.LocalIP, status.LocalIPv6 = firstPPPAddresses(addrs)
	}

	s.mu.Lock()
	s.connected = true
	s.mu.Unlock()

	return status, nil
}

func (s *PPPoEService) Connect(ctx context.Context) error {
	if err := s.renderConfig(); err != nil {
		return fmt.Errorf("render pppoe config: %w", err)
	}

	_, err := netutil.Run(ctx, "pppd", "call", "wan")
	if err != nil {
		return fmt.Errorf("start pppd: %w", err)
	}

	s.mu.Lock()
	s.connected = true
	hook := s.onConnect
	s.mu.Unlock()

	if hook != nil {
		if hookErr := hook(ctx); hookErr != nil {
			log.Printf("pppoe: onConnect hook failed: %v", hookErr)
		}
	}

	return nil
}

func (s *PPPoEService) Disconnect(ctx context.Context) error {
	pid, err := s.readPID()
	if err != nil {
		return fmt.Errorf("read pppd pid: %w", err)
	}

	if pid > 0 {
		_, err := netutil.Run(ctx, "kill", fmt.Sprintf("%d", pid))
		if err != nil {
			return fmt.Errorf("kill pppd: %w", err)
		}
	}

	s.mu.Lock()
	s.connected = false
	hook := s.onDisconnect
	s.mu.Unlock()

	if hook != nil {
		if hookErr := hook(ctx); hookErr != nil {
			log.Printf("pppoe: onDisconnect hook failed: %v", hookErr)
		}
	}

	return nil
}

func (s *PPPoEService) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected
}

type peerTemplateData struct {
	WANDevice       string
	Username        string
	MTU             int
	MRU             int
	LCPEchoInterval int
	LCPEchoFailure  int
	Holdoff         int
	IPv6CP          bool
}

// ErrInvalidPPPoECredentials reports a username or password that cannot
// be written into the pppd peer and secrets files as a single value.
var ErrInvalidPPPoECredentials = errors.New("invalid pppoe credentials")

// validatePPPoECredentials refuses what would let a value leave its
// quoted field. The username reaches `user "..."` in the peer file
// unescaped, where a newline starts a new pppd option (connect, plugin)
// that pppd runs as root, and a quote or backslash ends or escapes the
// field. Both values reach the secrets files through %q, which is safe
// for pppd only while they hold no control characters.
func validatePPPoECredentials(user, pass string) error {
	if strings.ContainsAny(user, `"\`) || strings.ContainsFunc(user, unicode.IsControl) {
		return fmt.Errorf("%w: username", ErrInvalidPPPoECredentials)
	}
	if strings.ContainsFunc(pass, unicode.IsControl) {
		return fmt.Errorf("%w: password", ErrInvalidPPPoECredentials)
	}
	return nil
}

func (s *PPPoEService) renderConfig() error {
	if err := validatePPPoECredentials(s.cfg.PPPoE.Username, s.cfg.PPPoE.Password); err != nil {
		return err
	}
	wanDevice := firstRoleDevice(s.cfg.Interfaces, "wan")
	if wanDevice == "" {
		return fmt.Errorf("no WAN interface configured")
	}

	// The agent creates the directory of the file it writes, and admits
	// no directory under /etc/ppp on its own.
	peerDir := "/etc/ppp/peers"

	optSrc, err := os.ReadFile("configs/sysconf/pppoe-options.tmpl")
	if err == nil {
		if err := netutil.WriteFile("/etc/ppp/options", optSrc, 0o644); err != nil {
			return fmt.Errorf("write /etc/ppp/options: %w", err)
		}
	}

	peer, err := renderPeerFile(s.peerData(wanDevice))
	if err != nil {
		return err
	}
	if err := netutil.WriteFile(filepath.Join(peerDir, "wan"), peer, 0o644); err != nil {
		return fmt.Errorf("write peer file: %w", err)
	}

	return s.writeSecrets()
}

// firstPPPAddresses returns the first IPv4 address and the first
// non-link-local IPv6 address, both without their prefix length.
func firstPPPAddresses(addrs []string) (ipv4, ipv6 string) {
	for _, addr := range addrs {
		if strings.Contains(addr, ".") && ipv4 == "" {
			ipv4 = strings.SplitN(addr, "/", 2)[0]
		} else if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "fe80") && ipv6 == "" {
			ipv6 = strings.SplitN(addr, "/", 2)[0]
		}
	}
	return ipv4, ipv6
}

// firstRoleDevice returns the device of the first interface with the
// given role, or "".
func firstRoleDevice(ifaces []config.InterfaceConfig, role string) string {
	for _, iface := range ifaces {
		if iface.Role == role {
			return iface.Device
		}
	}
	return ""
}

// peerData fills the peer template data, with defaults for the unset
// link parameters.
func (s *PPPoEService) peerData(wanDevice string) peerTemplateData {
	p := s.cfg.PPPoE
	return peerTemplateData{
		WANDevice:       wanDevice,
		Username:        p.Username,
		MTU:             cmp.Or(p.MTU, 1492),
		MRU:             cmp.Or(p.MRU, 1492),
		LCPEchoInterval: cmp.Or(p.LCPEchoInterval, 10),
		LCPEchoFailure:  cmp.Or(p.LCPEchoFailure, 3),
		Holdoff:         cmp.Or(p.Holdoff, 5),
		IPv6CP:          p.IPv6CP,
	}
}

// renderPeerFile executes the pppd peer template.
func renderPeerFile(data peerTemplateData) ([]byte, error) {
	tmpl, err := template.ParseFiles("configs/sysconf/pppoe-peer.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse peer template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute peer template: %w", err)
	}
	return buf.Bytes(), nil
}

// writeSecrets stores the credentials in the CHAP and PAP secrets files
// when a password is configured, replacing the line an earlier password
// left for the same user.
func (s *PPPoEService) writeSecrets() error {
	if s.cfg.PPPoE.Password == "" {
		return nil
	}
	prefix := fmt.Sprintf("%q * ", s.cfg.PPPoE.Username)
	secretsLine := fmt.Sprintf("%s%q\n", prefix, s.cfg.PPPoE.Password)
	if err := upsertSecret("/etc/ppp/chap-secrets", prefix, secretsLine); err != nil {
		return fmt.Errorf("write chap-secrets: %w", err)
	}
	if err := upsertSecret("/etc/ppp/pap-secrets", prefix, secretsLine); err != nil {
		return fmt.Errorf("write pap-secrets: %w", err)
	}
	return nil
}

func (s *PPPoEService) readPID() (int, error) {
	data, err := os.ReadFile("/var/run/ppp0.pid")
	if err != nil {
		return 0, err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return 0, fmt.Errorf("empty pid file")
	}

	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}

	return pid, nil
}

// processExists probes pid with signal 0. pppd runs as root and this
// process does not, so EPERM also means the process is alive.
// os.Signal(nil) is not the probe: Go refuses it before any syscall.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

type SniffStatus struct {
	Active       bool
	CapturedUser string
	CapturedPass string
}

func (s *PPPoEService) SniffStart(ctx context.Context) error {
	var wanDevice string
	for _, iface := range s.cfg.Interfaces {
		if iface.Role == "wan" {
			wanDevice = iface.Device
			break
		}
	}
	if wanDevice == "" {
		return fmt.Errorf("no WAN interface configured")
	}

	optSrc, err := os.ReadFile("configs/sysconf/pppoe-server-options.tmpl")
	if err != nil {
		return fmt.Errorf("read pppoe-server-options: %w", err)
	}
	if err := netutil.WriteFile("/etc/pppoe-server-options", optSrc, 0o644); err != nil {
		return fmt.Errorf("write pppoe-server-options: %w", err)
	}

	_ = os.Remove("/var/log/pppoe-sniff.log")

	_, err = netutil.Run(ctx, "pppoe-server", "-I", wanDevice, "-O", "/etc/pppoe-server-options", "-F")
	if err != nil {
		return fmt.Errorf("start pppoe-server: %w", err)
	}

	log.Printf("PPPoE sniff started on %s", wanDevice)
	return nil
}

func (s *PPPoEService) SniffStop(ctx context.Context) error {
	_, err := netutil.Run(ctx, "pkill", "-f", "pppoe-server")
	if err != nil {
		log.Printf("stop pppoe-server: %v", err)
	}
	log.Println("PPPoE sniff stopped")
	return nil
}

func (s *PPPoEService) SniffStatus() *SniffStatus {
	status := &SniffStatus{}

	_, err := netutil.RunSimple(context.Background(), "pgrep", "-f", "pppoe-server")
	status.Active = err == nil

	logData, err := os.ReadFile("/var/log/pppoe-sniff.log")
	if err == nil {
		lines := strings.SplitSeq(string(logData), "\n")
		for line := range lines {
			if strings.Contains(line, "user=") {
				for part := range strings.FieldsSeq(line) {
					if after, ok := strings.CutPrefix(part, "user="); ok {
						status.CapturedUser = after
						status.CapturedUser = strings.Trim(status.CapturedUser, "\"")
					}
				}
			}
			if strings.Contains(line, "PAP") && strings.Contains(line, "password") {
				for part := range strings.FieldsSeq(line) {
					if after, ok := strings.CutPrefix(part, "password="); ok {
						status.CapturedPass = after
						status.CapturedPass = strings.Trim(status.CapturedPass, "\"")
					}
				}
			}
		}
	}

	return status
}

// upsertSecret writes line into the pppd secrets file at path in place
// of every line that starts with prefix, the client name this service
// writes, and keeps all other lines. The ppp package ships both secrets
// files, so a read error is returned rather than taken for an empty file,
// which would overwrite every other entry.
func upsertSecret(path, prefix, line string) error {
	existing, err := netutil.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var b strings.Builder
	for l := range strings.Lines(string(existing)) {
		if !strings.HasPrefix(l, prefix) {
			b.WriteString(l)
		}
	}
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(line)
	return netutil.WriteFile(path, []byte(b.String()), 0o600)
}
