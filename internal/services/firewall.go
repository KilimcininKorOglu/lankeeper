package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type FirewallService struct {
	cfg     *config.Config
	mu      sync.RWMutex
	change  *netutil.AtomicChange
	tmpl    *template.Template
}

type nftTemplateData struct {
	LANInterfaces   []nftIface
	WANInterfaces   []nftIface
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
	WebPort           int
	IPv6Enabled       bool
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

	return &FirewallService{
		cfg:  cfg,
		tmpl: tmpl,
	}, nil
}

func NewFirewallServiceFromFS(cfg *config.Config, tmplContent string) (*FirewallService, error) {
	tmpl, err := template.New("nftables").Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("parse nftables template: %w", err)
	}

	return &FirewallService{
		cfg:  cfg,
		tmpl: tmpl,
	}, nil
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

	ac.StartWatchdog(30*time.Second, func() error {
		return ac.Rollback(context.Background())
	})

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
}

func (s *FirewallService) Rollback(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.change != nil {
		err := s.change.Rollback(ctx)
		s.change = nil
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

func (s *FirewallService) GenerateCustomNftRules() string {
	var sb strings.Builder

	for _, r := range s.cfg.Firewall.Rules {
		if !r.Enabled {
			continue
		}

		var conditions []string

		if r.Interface != "" {
			if r.Direction == "in" {
				conditions = append(conditions, fmt.Sprintf("iifname \"%s\"", r.Interface))
			} else {
				conditions = append(conditions, fmt.Sprintf("oifname \"%s\"", r.Interface))
			}
		}
		if r.SrcIP != "" {
			conditions = append(conditions, fmt.Sprintf("ip saddr %s", r.SrcIP))
		}
		if r.DstIP != "" {
			conditions = append(conditions, fmt.Sprintf("ip daddr %s", r.DstIP))
		}
		if r.Protocol != "" && r.Port > 0 {
			conditions = append(conditions, fmt.Sprintf("%s dport %d", r.Protocol, r.Port))
		} else if r.Protocol != "" {
			conditions = append(conditions, fmt.Sprintf("meta l4proto %s", r.Protocol))
		}

		action := r.Action
		if action == "" {
			action = "accept"
		}

		if len(conditions) > 0 {
			fmt.Fprintf(&sb, "        %s %s # %s\n",
				strings.Join(conditions, " "), action, r.Name)
		}
	}

	return sb.String()
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
