package services

import (
	"context"
	"encoding/json"
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
	PortForwards      []config.PortForward
	// Custom operator rules, already rendered and validated, grouped by
	// the chain they belong to. Placed ahead of the built-in accepts so
	// an explicit rule wins; a drop rule appended after them would never
	// match traffic the built-ins already accepted.
	CustomInputRules   []string
	CustomForwardRules []string
	CustomOutputRules  []string
	// OpenPortRules are the rendered accept lines for the operator's
	// open ports. They sit after the custom rules so an explicit custom
	// drop still wins over an opened port.
	OpenPortRules []string
	WebPort       int
	IPv6Enabled   bool
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

	remaining := max(firewallConfirmWindow-time.Since(state.AppliedAt), 0)

	ac := netutil.NewAtomicChangeWithSnapshot("firewall", state.Snapshot)
	s.change = ac
	s.armWatchdog(ac, remaining)

	log.Printf("firewall: restored unconfirmed change, rollback in %s", remaining)
}

// armWatchdog starts the revert timer and clears the persisted record
// once the revert has run. Leaving the record behind would make the next
// start believe a change is still pending that was already reverted.
//
// The callback takes the service lock before touching the change, so
// both it and the Confirm/Rollback methods acquire s.mu ahead of the
// change's own lock and cannot invert on each other.
func (s *FirewallService) armWatchdog(ac *netutil.AtomicChange, timeout time.Duration) {
	ac.StartWatchdog(timeout, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		// Stopping a timer that has already fired does not unschedule
		// the callback, so a Confirm racing the deadline can still land
		// here. The service no longer pointing at this change is what
		// says the change was already settled.
		if s.change != ac {
			return nil
		}
		s.change = nil

		err := ac.Rollback(context.Background())
		s.clearPendingState()
		return err
	})
}

// stopWatchdog disarms a pending rollback timer.
//
// The timer restorePendingChange arms runs for the remainder of the
// confirmation window, which is most of a minute, and nothing could
// stop it. It fires from its own goroutine into netutil.Run, so a
// service whose owner has gone away still issues privileged commands.
// The persisted record is left in place: it is what re-arms the
// rollback on the next start, which is the whole point of writing it.
func (s *FirewallService) stopWatchdog() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.change != nil {
		s.change.StopWatchdog()
	}
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

// ErrChangePending is returned when Apply is called while an earlier
// ruleset is still waiting for the operator to confirm it.
//
// Refusing is deliberate, rather than superseding the pending change.
// Apply renders from the live config, so a second apply reproduces the
// operator's already-persisted edit; the background caller that follows
// its apply with an immediate Confirm would therefore confirm that edit
// on the operator's behalf. If the edit was what cut their access, the
// watchdog was the only thing that would have brought it back.
var ErrChangePending = errors.New("a firewall change is still awaiting confirmation")

func (s *FirewallService) Apply(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// One watchdog at a time. A second apply would overwrite s.change
	// and leave the previous timer armed against an older snapshot, so
	// confirming the new change would still let the orphan revert both.
	if s.change != nil {
		return ErrChangePending
	}

	tmpFile, err := s.renderToFile()
	if err != nil {
		return fmt.Errorf("render nftables: %w", err)
	}
	ac := netutil.NewAtomicChange("firewall")

	// The snapshot IS the safety net. Applying without one arms a
	// watchdog that cannot revert anything, so a ruleset that locks the
	// operator out would stay in force while the log records a rollback
	// failure nobody is present to read. Refusing leaves the previous,
	// working ruleset untouched, which is the safe outcome.
	if err := ac.Snapshot(ctx); err != nil {
		return fmt.Errorf("snapshot current ruleset: %w", err)
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

// ErrInvalidTTL reports a hop limit outside what the field can carry.
var ErrInvalidTTL = errors.New("ttl value must be between 1 and 255")

// SetTTLFix records the TTL rewrite setting. The value is bounded here
// rather than at the handler because nftables parses the ruleset as a
// unit: one out-of-range literal fails the whole load, taking every
// other rule with it, and the caller sees a failure that names nothing
// in particular.
//
// This only writes the config. The rule reaches the kernel through the
// normal Apply path, so the change stays behind the confirmation
// watchdog instead of going live the moment a checkbox moves.
func (s *FirewallService) SetTTLFix(enabled bool, value int) error {
	if value < 1 || value > 255 {
		return fmt.Errorf("%w: %d", ErrInvalidTTL, value)
	}
	s.cfg.Firewall.TTLFix.Enabled = enabled
	s.cfg.Firewall.TTLFix.Value = value
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
// buildOpenPortRules renders every enabled open port. An entry that
// fails validation is skipped with a log line rather than aborting the
// whole ruleset, matching how custom rules are handled: one bad entry
// hand-edited into router.yaml must not take the firewall down.
func (s *FirewallService) buildOpenPortRules() []string {
	var out []string
	for _, op := range s.cfg.Firewall.OpenPorts {
		if !op.Enabled {
			continue
		}
		lines, err := renderOpenPortRules(op)
		if err != nil {
			log.Printf("firewall: skipping open port %q: %v", op.Name, err)
			continue
		}
		out = append(out, lines...)
	}
	return out
}

// openPortRateLimitPattern is the accepted form of a per-port rate,
// matching what nftables writes after `limit rate`.
//
// The value is interpolated straight into the ruleset, and nft parses
// the file as a whole, so one malformed rate does not fail its own line:
// it fails the entire load and takes every other rule with it. An
// allowlist is therefore the only safe treatment, not an escape.
var openPortRateLimitPattern = regexp.MustCompile(`^[1-9][0-9]{0,5}/(second|minute|hour|day|week)$`)

// ErrInvalidRateLimit rejects a rate before it reaches the ruleset.
var ErrInvalidRateLimit = errors.New(`rate limit must look like "3/minute" (second, minute, hour, day or week)`)

// ValidateOpenPortRateLimit reports whether s can be rendered after
// `limit rate`. An empty string is valid and means no limit.
func ValidateOpenPortRateLimit(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if !openPortRateLimitPattern.MatchString(s) {
		return ErrInvalidRateLimit
	}
	return nil
}

// renderOpenPortRules turns one entry into the input-chain accept lines
// it stands for. Protocol "both" yields one line per protocol so each
// rule stays a plain dport match.
func renderOpenPortRules(op config.OpenPort) ([]string, error) {
	if err := netutil.ValidatePort(op.Port); err != nil {
		return nil, err
	}

	protocols, err := openPortProtocols(op.Protocol)
	if err != nil {
		return nil, err
	}
	prefix, err := openPortSourcePrefix(op.Source)
	if err != nil {
		return nil, err
	}
	comment, err := openPortComment(op.Name)
	if err != nil {
		return nil, err
	}
	limit, err := openPortLimit(op.RateLimit)
	if err != nil {
		return nil, err
	}

	lines := make([]string, 0, len(protocols))
	for _, proto := range protocols {
		lines = append(lines, fmt.Sprintf("        %s%s dport %d ct state new %saccept%s",
			prefix, proto, op.Port, limit, comment))
	}
	return lines, nil
}

// openPortProtocols expands an open-port protocol into the protocols it
// renders as.
func openPortProtocols(protocol string) ([]string, error) {
	switch protocol {
	case "tcp", "udp":
		return []string{protocol}, nil
	case "both":
		return []string{"tcp", "udp"}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q", protocol)
	}
}

// openPortSourcePrefix returns the source match, with its trailing
// space, or "" when no source is set.
func openPortSourcePrefix(source string) (string, error) {
	src := strings.TrimSpace(source)
	if src == "" {
		return "", nil
	}
	if err := validateAddressOrCIDR(src); err != nil {
		return "", fmt.Errorf("source: %w", err)
	}
	return addressFamilyMatcher(src) + " saddr " + src + " ", nil
}

// openPortComment returns the trailing name comment, or "" when no name
// is set.
func openPortComment(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if err := netutil.ValidateRuleName(name); err != nil {
		return "", err
	}
	return " # " + name, nil
}

// openPortLimit returns the rate-limit clause, with its trailing space,
// or "" when no rate is set.
//
// It is placed after `ct state new` so the budget covers new connections
// rather than every packet of an established one. A packet over the rate
// simply fails to match this rule and falls through to the chain's
// closing drop, so no explicit drop line is needed.
func openPortLimit(rateLimit string) (string, error) {
	rate := strings.TrimSpace(rateLimit)
	if rate == "" {
		return "", nil
	}
	if err := ValidateOpenPortRateLimit(rate); err != nil {
		return "", fmt.Errorf("rate limit: %w", err)
	}
	return "limit rate " + rate + " ", nil
}

// addressFamilyMatcher picks the nftables address matcher for an IP or
// CIDR. The filter table is `inet`, so both families are valid in it,
// but `ip saddr` against an IPv6 address is a syntax error that nft
// rejects, which would take the whole ruleset with it.
func addressFamilyMatcher(s string) string {
	addr := s
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i]
	}
	if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
		return "ip6"
	}
	return "ip"
}

func renderCustomRule(r config.FirewallRule) (string, error) {
	if err := netutil.ValidateRuleName(r.Name); err != nil {
		return "", err
	}

	conditions, err := customRuleConditions(r)
	if err != nil {
		return "", err
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

// customRuleConditions renders the match expressions of a custom rule in
// order: interface, source, destination, protocol and port. Every value
// is validated first, because nft fails the whole file on one bad line.
func customRuleConditions(r config.FirewallRule) ([]string, error) {
	iface, err := interfaceCondition(r)
	if err != nil {
		return nil, err
	}
	src, err := addressCondition("source", "saddr", r.SrcIP)
	if err != nil {
		return nil, err
	}
	dst, err := addressCondition("destination", "daddr", r.DstIP)
	if err != nil {
		return nil, err
	}
	proto, err := protocolCondition(r)
	if err != nil {
		return nil, err
	}

	var conditions []string
	for _, cond := range []string{iface, src, dst, proto} {
		if cond != "" {
			conditions = append(conditions, cond)
		}
	}
	return conditions, nil
}

// interfaceCondition matches the rule's interface on the side its
// direction names, or returns "" when no interface is set.
func interfaceCondition(r config.FirewallRule) (string, error) {
	if r.Interface == "" {
		return "", nil
	}
	if err := netutil.ValidateInterfaceName(r.Interface); err != nil {
		return "", err
	}
	if r.Direction == "in" {
		return fmt.Sprintf("iifname %q", r.Interface), nil
	}
	return fmt.Sprintf("oifname %q", r.Interface), nil
}

// addressCondition matches addr with the matcher of its own family, or
// returns "" when addr is empty. label names the field in an error.
func addressCondition(label, selector, addr string) (string, error) {
	if addr == "" {
		return "", nil
	}
	if err := validateAddressOrCIDR(addr); err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	return fmt.Sprintf("%s %s %s", addressFamilyMatcher(addr), selector, addr), nil
}

// protocolCondition matches the protocol, with the destination port when
// one is set, or returns "" when no protocol is set.
func protocolCondition(r config.FirewallRule) (string, error) {
	if r.Protocol == "" {
		return "", nil
	}
	if r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "icmp" {
		return "", fmt.Errorf("unsupported protocol %q", r.Protocol)
	}
	if r.Port <= 0 {
		return fmt.Sprintf("meta l4proto %s", r.Protocol), nil
	}
	if err := netutil.ValidatePort(r.Port); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s dport %d", r.Protocol, r.Port), nil
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
	data.OpenPortRules = s.buildOpenPortRules()

	s.addSixInFour(data)
	s.addInterfaces(data)
	data.IsolatedVLANs = s.isolatedVLANs()
	s.addUSBTether(data)
	s.addVPNInterfaces(data)
	return data
}

// addSixInFour wires the 6in4 tunnel. When the operator selected mode
// "6in4" and provided at least the ServerIPv4 and a tunnel device, the
// sit interface is exposed as an IPv6-only WAN so LAN can forward to it,
// and the protocol-41 ingress rule is punched for the encapsulated
// traffic.
func (s *FirewallService) addSixInFour(data *nftTemplateData) {
	if s.cfg.IPv6.Mode != "6in4" || s.cfg.IPv6.Enabled == "off" {
		return
	}
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

// addInterfaces sorts the interfaces into WAN and LAN lists and records
// the first of each role as the primary device.
func (s *FirewallService) addInterfaces(data *nftTemplateData) {
	for _, iface := range s.cfg.Interfaces {
		switch iface.Role {
		case "wan":
			data.WANInterfaces = append(data.WANInterfaces, nftIface{Device: wanIPDevice(iface)})
		case "lan":
			data.LANInterfaces = append(data.LANInterfaces, nftIface{Device: iface.Device})
		}
	}
	data.WANDevice = firstDevice(data.WANInterfaces)
	data.LANDevice = firstDevice(data.LANInterfaces)
}

// wanIPDevice returns the interface that carries IP traffic for a WAN
// entry. A PPPoE WAN uses its configured NIC only as the carrier, and pppd
// runs the IP session over ppp0, so rules on the NIC never match.
func wanIPDevice(iface config.InterfaceConfig) string {
	if iface.Type == "pppoe" {
		return "ppp0"
	}
	return iface.Device
}

// firstDevice returns the first non-empty device name, or "".
func firstDevice(ifaces []nftIface) string {
	for _, iface := range ifaces {
		if iface.Device != "" {
			return iface.Device
		}
	}
	return ""
}

// isolatedVLANs lists the device of every isolated VLAN whose parent
// interface exists.
func (s *FirewallService) isolatedVLANs() []nftVLAN {
	var vlans []nftVLAN
	for _, vlan := range s.cfg.VLANs {
		if !vlan.Isolated {
			continue
		}
		if parentDev := s.interfaceDevice(vlan.Parent); parentDev != "" {
			vlans = append(vlans, nftVLAN{Device: fmt.Sprintf("%s.%d", parentDev, vlan.VID)})
		}
	}
	return vlans
}

// interfaceDevice returns the device of the interface with the given ID,
// or "" when no interface has it.
func (s *FirewallService) interfaceDevice(id string) string {
	return deviceByID(s.cfg.Interfaces, id)
}

// addUSBTether enables NAT out of the tethered interface, usb0 unless
// another is configured.
func (s *FirewallService) addUSBTether(data *nftTemplateData) {
	if !s.cfg.USBTether.Enabled || !s.cfg.USBTether.NAT {
		return
	}
	data.USBNATEnabled = true
	data.USBInterface = s.cfg.USBTether.Interface
	if data.USBInterface == "" {
		data.USBInterface = "usb0"
	}
}

// addVPNInterfaces lists the WireGuard server and client interfaces and
// the OpenVPN server device, tun0 unless another is configured.
func (s *FirewallService) addVPNInterfaces(data *nftTemplateData) {
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
}

// renderToFile stages the rendered ruleset where the agent can read it.
// The web process runs with PrivateTmp, so its own /tmp is invisible to
// the agent that runs nft.
func (s *FirewallService) renderToFile() (string, error) {
	rendered, err := s.RenderConfig()
	if err != nil {
		return "", err
	}
	path := filepath.Join(netutil.FirewallStagingDir(), "candidate.nft")
	if err := netutil.WriteFile(path, []byte(rendered), 0o600); err != nil {
		return "", fmt.Errorf("stage ruleset: %w", err)
	}
	return path, nil
}

func (s *FirewallService) RenderConfig() (string, error) {
	data := s.buildTemplateData()

	var buf = new(strings.Builder)
	if err := s.tmpl.Execute(buf, data); err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	return buf.String(), nil
}
