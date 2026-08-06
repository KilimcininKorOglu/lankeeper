package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// firewallConfirmWindow is how long an applied ruleset waits for the
// operator to confirm before the watchdog reverts it.
const firewallConfirmWindow = 30 * time.Second

const defaultFirewallStatePath = "/var/lib/lankeeper/firewall-pending.json"

// firewallPendingState is the on-disk record of an applied but
// unconfirmed ruleset. Without it the snapshot and the watchdog timer
// live only in process memory, and the web unit runs Restart=always
// with RestartSec=3, so a restart inside the window would strand a
// ruleset the watchdog exists to revert.
type firewallPendingState struct {
	Snapshot  string    `json:"snapshot"`
	AppliedAt time.Time `json:"appliedAt"`
}

type FirewallService struct {
	cfg       *config.Config
	mu        sync.RWMutex
	change    *netutil.AtomicChange
	tmpl      *template.Template
	statePath string
}

type nftTemplateData struct {
	LANInterfaces []nftIface
	WANInterfaces []nftIface
	// IPv6WANInterfaces lists IPv6-only WAN devices (today: the 6in4
	// sit interface). Forwarded for LAN ↔ tunnel traffic but
	// excluded from the IPv4 MASQUERADE block — there is no NAT66.
	IPv6WANInterfaces []nftIface
	LANDevice         string
	WANDevice         string
	IsolatedVLANs     []nftVLAN
	VLANDevice        string
	PortForwards      []config.PortForward
	RateLimits        map[string]string
	// Custom operator rules, already rendered and validated, grouped by
	// the chain they belong to. Placed ahead of the built-in accepts so
	// an explicit rule wins; a drop rule appended after them would never
	// match traffic the built-ins already accepted.
	CustomInputRules   []string
	CustomForwardRules []string
	CustomOutputRules  []string
	WebPort            int
	IPv6Enabled        bool
	// SixInFourEnabled gates the protocol-41 input rule. Set when
	// cfg.IPv6.Mode == "6in4" and ServerIPv4 is non-empty.
	SixInFourEnabled  bool
	SixInFourServer   string
	USBNATEnabled     bool
	USBInterface      string
	TTLFixEnabled     bool
	TTLFixValue       int
	WGServerEnabled   bool
	WGServerIface     string
	WGClientIfaces    []string
	OVPNServerEnabled bool
	OVPNServerIface   string
}

type nftIface struct {
	Device string
}

type nftVLAN struct {
	Device string
}

func NewFirewallService(cfg *config.Config) (*FirewallService, error) {
	tmpl, err := template.ParseFiles("configs/sysconf/nftables.conf.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse nftables template: %w", err)
	}

	return newFirewallService(cfg, tmpl), nil
}

func NewFirewallServiceFromFS(cfg *config.Config, tmplContent string) (*FirewallService, error) {
	tmpl, err := template.New("nftables").Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("parse nftables template: %w", err)
	}

	return newFirewallService(cfg, tmpl), nil
}

func newFirewallService(cfg *config.Config, tmpl *template.Template) *FirewallService {
	statePath := os.Getenv("LANKEEPER_FIREWALL_STATE")
	if statePath == "" {
		statePath = defaultFirewallStatePath
	}

	s := &FirewallService{
		cfg:       cfg,
		tmpl:      tmpl,
		statePath: statePath,
	}
	s.restorePendingChange()
	return s
}

// restorePendingChange re-arms the watchdog for a change this process
// did not apply. Called from the constructor so a restart inside the
// confirmation window still reverts an unconfirmed ruleset.
//
// The rollback runs from the timer goroutine, so the constructor returns
// immediately even when the deadline has already passed.
func (s *FirewallService) restorePendingChange() {
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return
	}

	var state firewallPendingState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("firewall: ignoring invalid pending-change file: %v", err)
		return
	}
	if state.Snapshot == "" {
		// Without a snapshot there is nothing to roll back to, so the
		// record is useless. Drop it rather than leaving it to be
		// re-read on every start.
		s.clearPendingState()
		return
	}

	remaining := firewallConfirmWindow - time.Since(state.AppliedAt)
	if remaining < 0 {
		remaining = 0
	}

	ac := netutil.NewAtomicChangeWithSnapshot("firewall", state.Snapshot)
	s.change = ac
	s.armWatchdog(ac, remaining)

	log.Printf("firewall: restored unconfirmed change, rollback in %s", remaining)
}

// armWatchdog starts the revert timer and clears the persisted record
// once the revert has run. Leaving the record behind would make the next
// start believe a change is still pending that was already reverted.
func (s *FirewallService) armWatchdog(ac *netutil.AtomicChange, timeout time.Duration) {
	ac.StartWatchdog(timeout, func() error {
		err := ac.Rollback(context.Background())
		s.clearPendingState()
		return err
	})
}

func (s *FirewallService) persistPendingState(snapshot string) {
	if snapshot == "" {
		// Apply logs its own warning when the snapshot could not be
		// taken. Persisting an empty record would only produce a file
		// that restore has to discard.
		return
	}

	data, err := json.Marshal(firewallPendingState{
		Snapshot:  snapshot,
		AppliedAt: time.Now(),
	})
	if err != nil {
		log.Printf("firewall: marshal pending state: %v", err)
		return
	}

	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o750); err != nil {
		log.Printf("firewall: create state dir: %v", err)
		return
	}
	if err := os.WriteFile(s.statePath, data, 0o600); err != nil {
		log.Printf("firewall: write pending state: %v", err)
	}
}

func (s *FirewallService) clearPendingState() {
	if err := os.Remove(s.statePath); err != nil && !os.IsNotExist(err) {
		log.Printf("firewall: remove pending state: %v", err)
	}
}

func (s *FirewallService) Apply(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tmpFile, err := s.renderToFile()
	if err != nil {
		return fmt.Errorf("render nftables: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile) }()

	ac := netutil.NewAtomicChange("firewall")

	if err := ac.Snapshot(ctx); err != nil {
		log.Printf("firewall snapshot failed (first apply?): %v", err)
	}

	if err := ac.Validate(ctx, tmpFile); err != nil {
		return fmt.Errorf("validate nftables: %w", err)
	}

	if err := ac.Apply(ctx, tmpFile); err != nil {
		return fmt.Errorf("apply nftables: %w", err)
	}

	s.change = ac
	s.persistPendingState(ac.GetSnapshot())
	s.armWatchdog(ac, firewallConfirmWindow)

	log.Println("firewall rules applied — waiting for confirmation (30s)")
	return nil
}

func (s *FirewallService) Confirm() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.change != nil {
		s.change.Confirm()
		s.change = nil
	}
	s.clearPendingState()
}

func (s *FirewallService) Rollback(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.change != nil {
		err := s.change.Rollback(ctx)
		s.change = nil
		s.clearPendingState()
		return err
	}
	return nil
}

func (s *FirewallService) GetRules(ctx context.Context) (string, error) {
	return netutil.RunSimple(ctx, "nft", "list", "ruleset")
}

func (s *FirewallService) persist() error {
	return s.cfg.SaveToFile()
}

func (s *FirewallService) AddOpenPort(op config.OpenPort) error {
	s.cfg.Firewall.OpenPorts = append(s.cfg.Firewall.OpenPorts, op)
	return s.persist()
}

func (s *FirewallService) RemoveOpenPort(index int) error {
	if index < 0 || index >= len(s.cfg.Firewall.OpenPorts) {
		return fmt.Errorf("invalid open port index: %d", index)
	}
	s.cfg.Firewall.OpenPorts = append(
		s.cfg.Firewall.OpenPorts[:index],
		s.cfg.Firewall.OpenPorts[index+1:]...,
	)
	return s.persist()
}

func (s *FirewallService) ToggleOpenPort(index int, enabled bool) error {
	if index < 0 || index >= len(s.cfg.Firewall.OpenPorts) {
		return fmt.Errorf("invalid open port index: %d", index)
	}
	s.cfg.Firewall.OpenPorts[index].Enabled = enabled
	return s.persist()
}

func (s *FirewallService) GetOpenPorts() []config.OpenPort {
	return s.cfg.Firewall.OpenPorts
}

func (s *FirewallService) AddPortForward(pf config.PortForward) error {
	s.cfg.Firewall.PortForwards = append(s.cfg.Firewall.PortForwards, pf)
	return s.persist()
}

func (s *FirewallService) RemovePortForward(index int) error {
	if index < 0 || index >= len(s.cfg.Firewall.PortForwards) {
		return fmt.Errorf("invalid port forward index: %d", index)
	}
	s.cfg.Firewall.PortForwards = append(
		s.cfg.Firewall.PortForwards[:index],
		s.cfg.Firewall.PortForwards[index+1:]...,
	)
	return s.persist()
}

func (s *FirewallService) AddRule(rule config.FirewallRule) error {
	if rule.Priority == 0 {
		maxPrio := 0
		for _, r := range s.cfg.Firewall.Rules {
			if r.Priority > maxPrio {
				maxPrio = r.Priority
			}
		}
		rule.Priority = maxPrio + 10
	}
	s.cfg.Firewall.Rules = append(s.cfg.Firewall.Rules, rule)
	return s.persist()
}

func (s *FirewallService) RemoveRule(index int) error {
	if index < 0 || index >= len(s.cfg.Firewall.Rules) {
		return fmt.Errorf("invalid rule index: %d", index)
	}
	s.cfg.Firewall.Rules = append(
		s.cfg.Firewall.Rules[:index],
		s.cfg.Firewall.Rules[index+1:]...,
	)
	return s.persist()
}

func (s *FirewallService) ToggleRule(index int, enabled bool) error {
	if index < 0 || index >= len(s.cfg.Firewall.Rules) {
		return fmt.Errorf("invalid rule index: %d", index)
	}
	s.cfg.Firewall.Rules[index].Enabled = enabled
	return s.persist()
}

func (s *FirewallService) GetCustomRules() []config.FirewallRule {
	return s.cfg.Firewall.Rules
}

// customRules holds the rendered custom rule lines, split by the chain
// each one targets.
type customRules struct {
	Input   []string
	Forward []string
	Output  []string
}

// buildCustomRules compiles cfg.Firewall.Rules into nftables lines,
// grouped by chain.
//
// Every field that reaches the output is validated here rather than
// only at the HTTP handler. The rendered file is plain text with no
// escaping, so a rule that arrived through hand-edited YAML, a restored
// backup, or a release that predates handler validation would otherwise
// be able to inject arbitrary nftables statements. An invalid rule is
// dropped and logged rather than silently rendered.
func (s *FirewallService) buildCustomRules() customRules {
	var out customRules

	for _, r := range s.cfg.Firewall.Rules {
		if !r.Enabled {
			continue
		}

		line, err := renderCustomRule(r)
		if err != nil {
			log.Printf("firewall: skipping custom rule %q: %v", r.Name, err)
			continue
		}
		if line == "" {
			continue
		}

		switch r.Chain {
		case "forward":
			out.Forward = append(out.Forward, line)
		case "output":
			out.Output = append(out.Output, line)
		default:
			out.Input = append(out.Input, line)
		}
	}

	return out
}

// renderCustomRule turns one rule into an nftables statement. It returns
// an empty string when the rule carries no match conditions, which would
// otherwise render as an unconditional accept or drop for the chain.
func renderCustomRule(r config.FirewallRule) (string, error) {
	if err := netutil.ValidateRuleName(r.Name); err != nil {
		return "", err
	}

	var conditions []string

	if r.Interface != "" {
		if err := netutil.ValidateInterfaceName(r.Interface); err != nil {
			return "", err
		}
		if r.Direction == "in" {
			conditions = append(conditions, fmt.Sprintf("iifname %q", r.Interface))
		} else {
			conditions = append(conditions, fmt.Sprintf("oifname %q", r.Interface))
		}
	}
	if r.SrcIP != "" {
		if err := validateAddressOrCIDR(r.SrcIP); err != nil {
			return "", fmt.Errorf("source: %w", err)
		}
		conditions = append(conditions, fmt.Sprintf("ip saddr %s", r.SrcIP))
	}
	if r.DstIP != "" {
		if err := validateAddressOrCIDR(r.DstIP); err != nil {
			return "", fmt.Errorf("destination: %w", err)
		}
		conditions = append(conditions, fmt.Sprintf("ip daddr %s", r.DstIP))
	}
	if r.Protocol != "" {
		if r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "icmp" {
			return "", fmt.Errorf("unsupported protocol %q", r.Protocol)
		}
		if r.Port > 0 {
			if err := netutil.ValidatePort(r.Port); err != nil {
				return "", err
			}
			conditions = append(conditions, fmt.Sprintf("%s dport %d", r.Protocol, r.Port))
		} else {
			conditions = append(conditions, fmt.Sprintf("meta l4proto %s", r.Protocol))
		}
	}

	action := r.Action
	if action == "" {
		action = "accept"
	}
	if action != "accept" && action != "drop" && action != "reject" {
		return "", fmt.Errorf("unsupported action %q", action)
	}

	if len(conditions) == 0 {
		return "", nil
	}

	line := fmt.Sprintf("        %s %s", strings.Join(conditions, " "), action)
	if r.Name != "" {
		line += " # " + r.Name
	}
	return line, nil
}

func validateAddressOrCIDR(s string) error {
	if netutil.ValidateCIDR(s) == nil {
		return nil
	}
	if netutil.ValidateIP(s) == nil {
		return nil
	}
	return fmt.Errorf("invalid address %q", s)
}

func (s *FirewallService) HasPendingChange() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.change != nil
}

func (s *FirewallService) buildTemplateData() *nftTemplateData {
	data := &nftTemplateData{
		PortForwards:  s.cfg.Firewall.PortForwards,
		RateLimits:    s.cfg.Firewall.RateLimits,
		WebPort:       s.cfg.System.WebPort,
		IPv6Enabled:   s.cfg.IPv6.Enabled != "off",
		TTLFixEnabled: s.cfg.Firewall.TTLFix.Enabled,
		TTLFixValue:   s.cfg.Firewall.TTLFix.Value,
	}

	if data.TTLFixValue == 0 {
		data.TTLFixValue = 64
	}

	custom := s.buildCustomRules()
	data.CustomInputRules = custom.Input
	data.CustomForwardRules = custom.Forward
	data.CustomOutputRules = custom.Output

	// 6in4 wiring: when the operator selected mode "6in4" and provided
	// at least the ServerIPv4 + a tunnel device, expose the sit
	// interface as an IPv6-only WAN so LAN can forward to it, and
	// punch the protocol-41 ingress rule for the encapsulated traffic.
	if s.cfg.IPv6.Mode == "6in4" && s.cfg.IPv6.Enabled != "off" {
		dev := strings.TrimSpace(s.cfg.IPv6.Tunnel.Device)
		if dev == "" {
			dev = "lkt6in4"
		}
		data.IPv6WANInterfaces = append(data.IPv6WANInterfaces, nftIface{Device: dev})
		if srv := strings.TrimSpace(s.cfg.IPv6.Tunnel.ServerIPv4); srv != "" {
			data.SixInFourEnabled = true
			data.SixInFourServer = srv
		}
	}

	for _, iface := range s.cfg.Interfaces {
		switch iface.Role {
		case "wan":
			data.WANInterfaces = append(data.WANInterfaces, nftIface{Device: iface.Device})
			if data.WANDevice == "" {
				data.WANDevice = iface.Device
			}
		case "lan":
			data.LANInterfaces = append(data.LANInterfaces, nftIface{Device: iface.Device})
			if data.LANDevice == "" {
				data.LANDevice = iface.Device
			}
		}
	}

	for _, vlan := range s.cfg.VLANs {
		if vlan.Isolated {
			var parentDev string
			for _, iface := range s.cfg.Interfaces {
				if iface.ID == vlan.Parent {
					parentDev = iface.Device
					break
				}
			}
			if parentDev != "" {
				data.IsolatedVLANs = append(data.IsolatedVLANs, nftVLAN{
					Device: fmt.Sprintf("%s.%d", parentDev, vlan.VID),
				})
			}
		}
	}

	if s.cfg.USBTether.Enabled && s.cfg.USBTether.NAT {
		data.USBNATEnabled = true
		data.USBInterface = s.cfg.USBTether.Interface
		if data.USBInterface == "" {
			data.USBInterface = "usb0"
		}
	}

	if s.cfg.VPN.Server.Enabled {
		data.WGServerEnabled = true
		data.WGServerIface = "wgs0"
	}
	for i := range s.cfg.VPN.Clients {
		data.WGClientIfaces = append(data.WGClientIfaces, fmt.Sprintf("wg%d", i))
	}

	if s.cfg.OpenVPN.Server.Enabled {
		data.OVPNServerEnabled = true
		data.OVPNServerIface = s.cfg.OpenVPN.Server.Device
		if data.OVPNServerIface == "" {
			data.OVPNServerIface = "tun0"
		}
	}

	return data
}

func (s *FirewallService) renderToFile() (string, error) {
	data := s.buildTemplateData()

	f, err := os.CreateTemp("", "nftables-*.conf")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}

	if err := s.tmpl.Execute(f, data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("execute template: %w", err)
	}

	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close temp: %w", err)
	}
	return f.Name(), nil
}

func (s *FirewallService) RenderConfig() (string, error) {
	data := s.buildTemplateData()

	var buf = new(strings.Builder)
	if err := s.tmpl.Execute(buf, data); err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	return buf.String(), nil
}
