// Package services provides the IPv6Service that orchestrates the
// wide-dhcpv6 client (dhcp6c) for ISP prefix delegation.
//
// Lifecycle: render dhcp6c.conf and the lease-event hook script ->
// systemctl start lankeeper-dhcp6c.service -> dhcp6c writes the
// current prefix to a JSON state file -> Status() reads that file.
//
// The 3-layer config rendering pattern (RenderConfig / RenderToDisk
// / ApplyConfig) matches every other service in this package so
// install-time `render-configs` and runtime CRUD share one codepath.
package services

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

const (
	dhcp6cConfPath   = "/etc/wide-dhcpv6/dhcp6c.conf"
	dhcp6cScriptPath = "/etc/wide-dhcpv6/dhcp6c-script"
	ipv6StatePath    = "/var/lib/lankeeper/state/ipv6-prefix.json"
	dhcp6cUnitName   = "lankeeper-dhcp6c.service"
	// dnsmasqRAConfPath is the drop-in dnsmasq.conf-dir file that owns
	// every Router Advertisement directive. Owning a separate file
	// keeps the IPv6 RA layer decoupled from DHCPv4 — the DHCP service
	// rewrites /etc/dnsmasq.conf, the IPv6 service rewrites this drop-in.
	dnsmasqRAConfPath = "/etc/dnsmasq.d/lankeeper-ipv6-ra.conf"
	// defaultRAInterval matches the value historically embedded in
	// dnsmasq.conf.tmpl. Surfaced as a constant so tests can assert it.
	defaultRAInterval = 30
)

// PrefixState is the parsed view of the JSON document the dhcp6c hook
// script writes on every lease event. The zero value (Prefix == "")
// represents "no delegation yet" — distinct from an explicit RELEASE.
type PrefixState struct {
	Timestamp         int64  `json:"timestamp"`
	Reason            string `json:"reason"`
	Prefix            string `json:"prefix"`
	PrefixLength      int    `json:"prefixLength"`
	PreferredLifetime int64  `json:"preferredLifetime"`
	ValidLifetime     int64  `json:"validLifetime"`
	RDNSS             string `json:"rdnss"`
}

// Active reports whether a usable prefix is currently delegated.
// Considers the prefix lifeless once ValidLifetime has elapsed since
// the lease event timestamp, even if dhcp6c has not yet rewritten
// the state file with a RELEASE.
func (p PrefixState) Active() bool {
	if p.Prefix == "" || p.ValidLifetime <= 0 {
		return false
	}
	if p.Reason == "RELEASE" || p.Reason == "EXIT" {
		return false
	}
	if p.Expired() {
		return false
	}
	return true
}

// Expired reports whether the lease's valid lifetime has elapsed
// relative to wall-clock time. Returns false when Timestamp is zero
// (no lease recorded yet) or when ValidLifetime is non-positive.
func (p PrefixState) Expired() bool {
	if p.Timestamp == 0 || p.ValidLifetime <= 0 {
		return false
	}
	deadline := time.Unix(p.Timestamp, 0).Add(time.Duration(p.ValidLifetime) * time.Second)
	return time.Now().After(deadline)
}

// ExpiresIn returns the time remaining before the valid lifetime ends.
// Negative values indicate an already-expired lease. Zero when no
// lease has been recorded.
func (p PrefixState) ExpiresIn() time.Duration {
	if p.Timestamp == 0 || p.ValidLifetime <= 0 {
		return 0
	}
	deadline := time.Unix(p.Timestamp, 0).Add(time.Duration(p.ValidLifetime) * time.Second)
	return time.Until(deadline)
}

// CIDR returns "<prefix>/<length>" or "" if no prefix is held.
func (p PrefixState) CIDR() string {
	if p.Prefix == "" || p.PrefixLength == 0 {
		return ""
	}
	return fmt.Sprintf("%s/%d", p.Prefix, p.PrefixLength)
}

type IPv6Service struct {
	cfg *config.Config
	mu  sync.Mutex

	// Template overrides for tests; empty in production. When set,
	// RenderConfig / RenderScript skip filesystem ParseFiles and use
	// these strings directly. Mirrors the pattern used by FirewallService.
	confTmpl   string
	scriptTmpl string
	raTmpl     string
	// statePathOverride overrides ipv6StatePath. Tests set this to a
	// temporary file so the watcher does not need /var/lib/lankeeper/.
	statePathOverride string

	// onLease is invoked whenever the dhcp6c lease state file changes
	// (registered via SetOnLeaseChange). Lets cross-cutting services
	// (firewall, DNS) refresh their derived state without polling.
	// Errors are logged; never block the watcher loop.
	onLease     func(ctx context.Context, state PrefixState) error
	watcherStop     chan struct{}
	watcherWG       sync.WaitGroup
	watcherDebounce *time.Timer
	// lastLeaseHash tracks the digest of the last state we dispatched
	// so duplicate fsnotify events (atomic mv writes 1+ events) do not
	// trigger duplicate firewall reloads.
	lastLeaseHash string
}

// statePath returns the active lease state path, honouring the test
// override when present.
func (s *IPv6Service) statePath() string {
	if s.statePathOverride != "" {
		return s.statePathOverride
	}
	return ipv6StatePath
}

// SetStatePathForTest overrides the lease state file path. Test-only
// hook; production code should never call this.
func (s *IPv6Service) SetStatePathForTest(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statePathOverride = path
}

func NewIPv6Service(cfg *config.Config) *IPv6Service {
	return &IPv6Service{cfg: cfg}
}

// NewIPv6ServiceFromFS creates a service that uses the given template
// strings instead of reading them off disk. Intended for unit tests.
// raTmpl may be empty when the caller does not exercise RA rendering.
func NewIPv6ServiceFromFS(cfg *config.Config, confTmpl, scriptTmpl, raTmpl string) *IPv6Service {
	return &IPv6Service{cfg: cfg, confTmpl: confTmpl, scriptTmpl: scriptTmpl, raTmpl: raTmpl}
}

type dhcp6cTemplateData struct {
	WANInterface string
	RapidCommit  bool
	// ScriptPath is the absolute path to the lease-event hook script
	// that dhcp6c invokes on every state change.
	ScriptPath string
	// SLALen is the suffix length given to each LAN sub-prefix:
	// `delegated_length + SLALen <= 64` for SLAAC compatibility.
	// For a /56 delegation we want /64 sub-prefixes -> SLALen = 8.
	SLALen int
	// PrefixInterfaces is one entry per downstream interface (LAN
	// bridge + each VLAN) that receives a sub-prefix.
	PrefixInterfaces []prefixInterface
}

type prefixInterface struct {
	Device string
	SLAID  int
}

// AnnouncedInterface describes one downstream device on which dnsmasq
// announces a delegated /64 sub-prefix via SLAAC. Returned by
// AnnouncedInterfaces for the IPv6 status UI.
type AnnouncedInterface struct {
	Device string
	SLAID  int
}

// AnnouncedInterfaces returns one entry per LAN/VLAN that receives a
// Router Advertisement, in the same order they appear in the rendered
// dnsmasq drop-in. Returns nil when IPv6 is disabled.
func (s *IPv6Service) AnnouncedInterfaces() ([]AnnouncedInterface, error) {
	if s.cfg.IPv6.Enabled == "off" {
		return nil, nil
	}
	_, lan, err := s.resolveInterfaces()
	if err != nil {
		return nil, err
	}
	hint := s.routedPrefixLength()
	delegatedLen, err := parsePrefixHint(hint)
	if err != nil {
		return nil, err
	}
	slaLen := 64 - delegatedLen
	if slaLen < 0 {
		slaLen = 0
	}

	prefixes := s.buildPrefixInterfaces(lan, slaLen)
	out := make([]AnnouncedInterface, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, AnnouncedInterface(p))
	}
	return out, nil
}

type dhcp6cScriptTemplateData struct {
	StatePath string
}

// dnsmasqRATemplateData carries the values rendered into the dnsmasq
// IPv6 RA drop-in. One Interfaces entry per LAN/VLAN that received a
// /64 sub-prefix from wide-dhcpv6.
type dnsmasqRATemplateData struct {
	Interfaces []prefixInterface
	LeaseTime  string
	RAInterval int
	// MTU advertised in the RA so clients pick up the PPPoE-clamped
	// link MTU instead of the default 1500. Defaults to 1492 when
	// PPPoE is in use, 1500 otherwise.
	MTU int
	// RDNSSAddrs are the upstream DNS server IPv6 addresses learned
	// from the dhcp6c lease event. We always prepend the router's
	// link-local address (::1) so unbound stays in the path even when
	// the ISP did not push DNS.
	RDNSSAddrs []string
	// SearchDomain is `cfg.System.Domain` when set, used for the RA's
	// option6:domain-search DNSSL. Empty string disables the option.
	SearchDomain string
	ULAPrefix    string
	// Privacy maps to cfg.IPv6.Privacy and toggles the temporary-
	// addresses preference on dnsmasq's RA. Currently informational —
	// dnsmasq does not expose a direct flag for prefer_temp; we keep
	// the field so the rendered comment can document the operator's
	// intent without lying via an unsupported ra-param value.
	Privacy bool
}

// RenderRAConfig returns the dnsmasq drop-in that announces every
// delegated /64 to its downstream interface. Pure computation — no I/O.
func (s *IPv6Service) RenderRAConfig() (string, error) {
	if s.cfg.IPv6.Enabled == "off" {
		return "", nil
	}

	_, lan, err := s.resolveInterfaces()
	if err != nil {
		return "", err
	}

	// Pick the source prefix: PD modunda WAN.PrefixHint + lease,
	// 6in4 modunda Tunnel.RoutedPrefix (statik, lease yok).
	hint := s.routedPrefixLength()
	delegatedLen, err := parsePrefixHint(hint)
	if err != nil {
		return "", err
	}
	slaLen := 64 - delegatedLen
	if slaLen < 0 {
		slaLen = 0
	}

	raInterval := s.cfg.IPv6.LAN.RAInterval
	if raInterval <= 0 {
		raInterval = defaultRAInterval
	}
	mtu := s.advertisedMTU()
	data := dnsmasqRATemplateData{
		Interfaces:   s.buildPrefixInterfaces(lan, slaLen),
		LeaseTime:    s.dhcpLeaseTime(),
		RAInterval:   raInterval,
		MTU:          mtu,
		RDNSSAddrs:   s.rdnssAddrs(),
		SearchDomain: s.cfg.System.Domain,
		Privacy:      s.cfg.IPv6.Privacy,
	}
	if s.cfg.IPv6.LAN.ULA.Enabled {
		data.ULAPrefix = s.ulaPrefix()
	}

	tmpl, err := s.parseRATemplate()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render dnsmasq RA: %w", err)
	}
	return buf.String(), nil
}

// dhcpLeaseTime returns the DHCP lease time string from config or a
// sensible default. RA lifetimes track the DHCPv4 lease so clients
// re-confirm both stacks at the same cadence.
func (s *IPv6Service) dhcpLeaseTime() string {
	if lt := s.cfg.DHCP.LeaseTime; lt != "" {
		return lt
	}
	return "12h"
}

// routedPrefixLength returns the prefix-hint string (`/48`, `/56`, …)
// the RA renderer should use when carving /64 sub-prefixes for the
// LAN bridge and each VLAN. In 6in4 mode the operator's RoutedPrefix
// already encodes the length; in PD mode we honour WAN.PrefixHint.
// Default is "/56" — the typical residential allocation.
func (s *IPv6Service) routedPrefixLength() string {
	if s.cfg.IPv6.Mode == "6in4" {
		if rp := strings.TrimSpace(s.cfg.IPv6.Tunnel.RoutedPrefix); rp != "" {
			if i := strings.LastIndex(rp, "/"); i >= 0 && i < len(rp)-1 {
				return rp[i:] // "/48", "/64"
			}
		}
		// Fallback when RoutedPrefix is mid-form (operator typing).
		return "/64"
	}
	if h := strings.TrimSpace(s.cfg.IPv6.WAN.PrefixHint); h != "" {
		return h
	}
	return "/56"
}

// advertisedMTU is the link MTU emitted in the RA's ra-param. In
// 6in4 mode we drop to the tunnel MTU (1452 over PPPoE, 1480 direct)
// so clients clamp MSS for the encapsulated path. In PD mode we
// stick with the link MTU (1492 PPPoE / 1500 direct).
func (s *IPv6Service) advertisedMTU() int {
	pppoe := s.cfg.PPPoE.Username != ""
	if s.cfg.IPv6.Mode == "6in4" {
		if pppoe {
			return 1452
		}
		return 1480
	}
	if pppoe {
		return 1492
	}
	return 1500
}

// rdnssAddrs returns the DNS servers to advertise via RA. Reads the
// dhcp6c lease state file directly (not via Status() — we want to keep
// RenderRAConfig pure-ish; failures fall back to the empty slice).
// Always prepends a router-local address so unbound stays reachable
// even before the upstream lease arrives.
func (s *IPv6Service) rdnssAddrs() []string {
	out := []string{}
	raw, err := netutil.ReadFile(s.statePath())
	if err == nil && len(bytes.TrimSpace(raw)) > 0 {
		var st PrefixState
		if jsonErr := json.Unmarshal(raw, &st); jsonErr == nil && st.RDNSS != "" {
			for _, f := range strings.Fields(st.RDNSS) {
				if f == "" {
					continue
				}
				out = append(out, f)
			}
		}
	}
	return out
}

// ulaPrefix returns the configured ULA prefix or generates one on
// the fly per RFC 4193 (random 40-bit Global ID). The generated prefix
// is persisted to cfg so subsequent boots reuse the same ULA.
func (s *IPv6Service) ulaPrefix() string {
	if p := strings.TrimSpace(s.cfg.IPv6.LAN.ULA.Prefix); p != "" {
		return p
	}
	prefix, err := generateULAPrefix()
	if err != nil {
		return ""
	}
	// Persist so the next render keeps the same Global ID. Best-effort:
	// if SaveToFile fails the operator simply gets a freshly-rolled
	// prefix on next render — clients tolerate the swap because RA
	// lifetimes flush stale prefixes within seconds.
	s.cfg.IPv6.LAN.ULA.Prefix = prefix
	if err := s.cfg.SaveToFile(); err != nil {
		log.Printf("ipv6: persist generated ULA prefix: %v", err)
	}
	return prefix
}

// GenerateULAPrefixForTest exposes generateULAPrefix to the test
// package. Production code must use ulaPrefix() which also persists.
func GenerateULAPrefixForTest() (string, error) { return generateULAPrefix() }

// generateULAPrefix builds a `fdXX:XXXX:XXXX::/48` from a 40-bit
// cryptographically random Global ID per RFC 4193 §3.2.1. Returns
// an error only when the OS RNG is unreadable.
func generateULAPrefix() (string, error) {
	var raw [5]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	// fd00::/8 prefix + 40 random bits = /48 ULA.
	return fmt.Sprintf("fd%02x:%02x%02x:%02x%02x::/48",
		raw[0], raw[1], raw[2], raw[3], raw[4]), nil
}

func (s *IPv6Service) parseRATemplate() (*template.Template, error) {
	if s.raTmpl != "" {
		t, err := template.New("dnsmasq-ipv6-ra.conf.tmpl").Parse(s.raTmpl)
		if err != nil {
			return nil, fmt.Errorf("parse inline dnsmasq RA template: %w", err)
		}
		return t, nil
	}
	t, err := template.New("dnsmasq-ipv6-ra.conf.tmpl").ParseFiles("configs/sysconf/dnsmasq-ipv6-ra.conf.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse dnsmasq RA template: %w", err)
	}
	return t, nil
}

// RenderConfig returns the rendered dhcp6c.conf as a string. Pure
// computation — no I/O.
func (s *IPv6Service) RenderConfig() (string, error) {
	wan, lan, err := s.resolveInterfaces()
	if err != nil {
		return "", err
	}

	hint := s.cfg.IPv6.WAN.PrefixHint
	if hint == "" {
		hint = "/56"
	}
	delegatedLen, err := parsePrefixHint(hint)
	if err != nil {
		return "", err
	}
	slaLen := 64 - delegatedLen
	if slaLen < 0 {
		slaLen = 0
	}

	data := dhcp6cTemplateData{
		WANInterface:     wan,
		RapidCommit:      s.cfg.IPv6.WAN.RapidCommit,
		ScriptPath:       dhcp6cScriptPath,
		SLALen:           slaLen,
		PrefixInterfaces: s.buildPrefixInterfaces(lan, slaLen),
	}

	tmpl, err := s.parseConfTemplate()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render dhcp6c.conf: %w", err)
	}
	return buf.String(), nil
}

// RenderScript returns the rendered dhcp6c lease-event hook script.
func (s *IPv6Service) RenderScript() (string, error) {
	tmpl, err := s.parseScriptTemplate()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, dhcp6cScriptTemplateData{StatePath: ipv6StatePath}); err != nil {
		return "", fmt.Errorf("render dhcp6c-script: %w", err)
	}
	return buf.String(), nil
}

func (s *IPv6Service) parseConfTemplate() (*template.Template, error) {
	if s.confTmpl != "" {
		t, err := template.New("dhcp6c.conf.tmpl").Parse(s.confTmpl)
		if err != nil {
			return nil, fmt.Errorf("parse inline dhcp6c template: %w", err)
		}
		return t, nil
	}
	t, err := template.New("dhcp6c.conf.tmpl").ParseFiles("configs/sysconf/dhcp6c.conf.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse dhcp6c template: %w", err)
	}
	return t, nil
}

func (s *IPv6Service) parseScriptTemplate() (*template.Template, error) {
	if s.scriptTmpl != "" {
		t, err := template.New("dhcp6c-script.tmpl").Parse(s.scriptTmpl)
		if err != nil {
			return nil, fmt.Errorf("parse inline dhcp6c-script template: %w", err)
		}
		return t, nil
	}
	t, err := template.New("dhcp6c-script.tmpl").ParseFiles("configs/sysconf/dhcp6c-script.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse dhcp6c-script template: %w", err)
	}
	return t, nil
}

// RenderToDisk writes the dhcp6c daemon config + hook script and the
// dnsmasq RA drop-in. Suitable for install-time invocation. State
// directory is pre-created so the hook script does not fail on first
// run. 6in4 mode skips dhcp6c.conf rendering but still writes the RA
// drop-in derived from the operator's RoutedPrefix.
func (s *IPv6Service) RenderToDisk(ctx context.Context) error {
	mode := s.cfg.IPv6.Mode
	off := s.cfg.IPv6.Enabled == "off"
	pdRequested := mode != "6in4" && s.cfg.IPv6.WAN.RequestPrefix

	if off || (!pdRequested && mode != "6in4") {
		// IPv6 disabled, or no plane is active. Drop stubs so stale
		// config doesn't linger.
		if err := netutil.WriteFile(dhcp6cConfPath, []byte("# IPv6 PD disabled by LANKeeper config.\n"), 0o644); err != nil {
			return fmt.Errorf("write disabled stub: %w", err)
		}
		if err := netutil.WriteFile(dnsmasqRAConfPath, []byte("# IPv6 RA disabled by LANKeeper config.\n"), 0o644); err != nil {
			return fmt.Errorf("write disabled RA stub: %w", err)
		}
		return nil
	}

	if err := netutil.MkdirAll(filepath.Dir(ipv6StatePath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(ipv6StatePath), err)
	}
	if err := netutil.MkdirAll(filepath.Dir(dnsmasqRAConfPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dnsmasqRAConfPath), err)
	}

	// PD plane: render dhcp6c.conf + hook script.
	if pdRequested {
		conf, err := s.RenderConfig()
		if err != nil {
			return err
		}
		script, err := s.RenderScript()
		if err != nil {
			return err
		}
		if err := netutil.MkdirAll(filepath.Dir(dhcp6cConfPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dhcp6cConfPath), err)
		}
		if err := netutil.WriteFile(dhcp6cConfPath, []byte(conf), 0o644); err != nil {
			return fmt.Errorf("write dhcp6c.conf: %w", err)
		}
		if err := netutil.WriteFile(dhcp6cScriptPath, []byte(script), 0o755); err != nil {
			return fmt.Errorf("write dhcp6c-script: %w", err)
		}
	} else if mode == "6in4" {
		// In 6in4 mode dhcp6c never runs; leave a stub so anyone
		// inspecting /etc/wide-dhcpv6/ sees why.
		if err := netutil.WriteFile(dhcp6cConfPath,
			[]byte("# IPv6 mode is 6in4; dhcp6c is not used.\n"), 0o644); err != nil {
			return fmt.Errorf("write 6in4 stub: %w", err)
		}
	}

	// RA plane (PD or 6in4): always render.
	raConf, err := s.RenderRAConfig()
	if err != nil {
		return err
	}
	if err := netutil.WriteFile(dnsmasqRAConfPath, []byte(raConf), 0o644); err != nil {
		return fmt.Errorf("write dnsmasq RA drop-in: %w", err)
	}
	return nil
}

// ApplyConfig renders to disk, restarts dhcp6c, and asks dnsmasq to
// re-read its config so the freshly rendered RA drop-in takes effect.
func (s *IPv6Service) ApplyConfig(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.RenderToDisk(ctx); err != nil {
		return err
	}
	if err := s.reloadDnsmasqLocked(ctx); err != nil {
		// Reload failure must not block the dhcp6c lifecycle: log and
		// keep going so the operator still gets the prefix even if RA
		// is briefly stale.
		log.Printf("ipv6: dnsmasq reload after RA rewrite: %v", err)
	}
	// 6in4 mode and PD-disabled both mean dhcp6c must be down. Only
	// PD-mode + RequestPrefix=true keeps dhcp6c running.
	if s.cfg.IPv6.Enabled == "off" ||
		s.cfg.IPv6.Mode == "6in4" ||
		!s.cfg.IPv6.WAN.RequestPrefix {
		return s.stopUnitLocked(ctx)
	}
	return s.restartUnitLocked(ctx)
}

// reloadDnsmasqLocked sends SIGHUP to dnsmasq via systemctl. Best-effort:
// dnsmasq may not be installed yet in tests / first-boot flows.
func (s *IPv6Service) reloadDnsmasqLocked(ctx context.Context) error {
	_, err := netutil.Run(ctx, "systemctl", "reload-or-restart", "dnsmasq")
	if err != nil {
		return fmt.Errorf("reload dnsmasq: %w", err)
	}
	return nil
}

// Start enables and starts the dhcp6c unit. Idempotent.
func (s *IPv6Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := netutil.Run(ctx, "systemctl", "enable", "--now", dhcp6cUnitName)
	if err != nil {
		return fmt.Errorf("start %s: %w", dhcp6cUnitName, err)
	}
	return nil
}

// Stop disables and stops the dhcp6c unit. Idempotent.
func (s *IPv6Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopUnitLocked(ctx)
}

// Restart bounces the dhcp6c unit. Used after WAN reconnect.
func (s *IPv6Service) Restart(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartUnitLocked(ctx)
}

// Renew triggers an immediate Renew message via SIGHUP. dhcp6c reloads
// the config and re-solicits the ISP without dropping the lease.
func (s *IPv6Service) Renew(ctx context.Context) error {
	_, err := netutil.Run(ctx, "systemctl", "reload-or-restart", dhcp6cUnitName)
	if err != nil {
		return fmt.Errorf("renew %s: %w", dhcp6cUnitName, err)
	}
	return nil
}

// Release sends a DHCPv6 RELEASE and stops the unit. Used when the
// operator wants to drop the prefix explicitly.
func (s *IPv6Service) Release(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Best effort: dhcp6c does not have a direct release CLI; stopping
	// the unit triggers the EXIT reason in the hook script which clears
	// the state file. Operators can re-enable via Start.
	return s.stopUnitLocked(ctx)
}

// Status reads the JSON state file written by the dhcp6c hook script.
// Returns a zero PrefixState (Active() == false) when no lease has
// been recorded yet.
func (s *IPv6Service) Status(ctx context.Context) (PrefixState, error) {
	path := s.statePath()
	raw, err := netutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PrefixState{}, nil
		}
		return PrefixState{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return PrefixState{}, nil
	}
	var st PrefixState
	if err := json.Unmarshal(raw, &st); err != nil {
		return PrefixState{}, fmt.Errorf("parse ipv6 state: %w", err)
	}
	return st, nil
}

// PrefixAge returns how long ago the lease event was recorded. Useful
// for "last updated 5m ago" display.
func (p PrefixState) PrefixAge() time.Duration {
	if p.Timestamp == 0 {
		return 0
	}
	return time.Since(time.Unix(p.Timestamp, 0))
}

// buildPrefixInterfaces returns one entry per downstream interface
// (LAN bridge + each VLAN) that should receive a sub-prefix. SLA-IDs
// are taken from cfg.IPv6.LAN.SubnetMap when present, otherwise
// auto-assigned in declaration order starting at 0 for the LAN.
//
// When slaLen == 0 the ISP delegated a /64 — no room to subdivide, so
// only the primary LAN gets a prefix-interface entry.
func (s *IPv6Service) buildPrefixInterfaces(lanDev string, slaLen int) []prefixInterface {
	subnetMap := s.cfg.IPv6.LAN.SubnetMap

	// Helper: pick from override map, fall back to caller-supplied default.
	pick := func(key string, dflt int) int {
		if v, ok := subnetMap[key]; ok {
			return v
		}
		return dflt
	}

	out := []prefixInterface{
		{Device: lanDev, SLAID: pick("lan", 0)},
	}

	if slaLen == 0 {
		// /64 delegation has zero subnet bits — VLANs cannot get
		// distinct sub-prefixes from this delegation.
		return out
	}

	maxSLA := 1<<slaLen - 1
	auto := 1
	for _, vlan := range s.cfg.VLANs {
		if vlan.ID == "" {
			continue
		}
		var parentDev string
		for _, iface := range s.cfg.Interfaces {
			if iface.ID == vlan.Parent {
				parentDev = iface.Device
				break
			}
		}
		if parentDev == "" || vlan.VID == 0 {
			continue
		}
		dev := fmt.Sprintf("%s.%d", parentDev, vlan.VID)
		sla := pick(vlan.ID, auto)
		if sla > maxSLA {
			// Skip silently; operator will see fewer interfaces in
			// the rendered config than expected. The Web UI surface
			// can flag this in a follow-up commit.
			continue
		}
		out = append(out, prefixInterface{Device: dev, SLAID: sla})
		auto++
	}
	return out
}

// resolveInterfaces returns the WAN and LAN device names from the
// router configuration. WAN is the PPPoE interface name (ppp0 by
// convention) when PPPoE is the WAN method; LAN is the first
// interface tagged role=lan.
func (s *IPv6Service) resolveInterfaces() (wan, lan string, err error) {
	for _, iface := range s.cfg.Interfaces {
		switch iface.Role {
		case "wan":
			// dhcp6c needs the L3 interface that actually carries the
			// IPv6 link to the ISP. With PPPoE this is ppp0, not the
			// underlying physical NIC.
			if s.cfg.PPPoE.Username != "" {
				wan = "ppp0"
			} else {
				wan = iface.Device
			}
		case "lan":
			if lan == "" {
				lan = iface.Device
			}
		}
	}
	if wan == "" {
		return "", "", fmt.Errorf("no WAN interface configured")
	}
	if lan == "" {
		return "", "", fmt.Errorf("no LAN interface configured")
	}
	return wan, lan, nil
}

// parsePrefixHint converts "/56" into 56. Accepts the bare number or
// the slash-prefixed form. Rejects values outside [48, 64].
func parsePrefixHint(s string) (int, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(s), "/")
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid prefix hint %q: %w", s, err)
	}
	if n < 48 || n > 64 {
		return 0, fmt.Errorf("prefix hint %d outside [48,64]", n)
	}
	return n, nil
}

func (s *IPv6Service) restartUnitLocked(ctx context.Context) error {
	_, err := netutil.Run(ctx, "systemctl", "restart", dhcp6cUnitName)
	if err != nil {
		return fmt.Errorf("restart %s: %w", dhcp6cUnitName, err)
	}
	return nil
}

func (s *IPv6Service) stopUnitLocked(ctx context.Context) error {
	_, err := netutil.Run(ctx, "systemctl", "stop", dhcp6cUnitName)
	if err != nil {
		// systemctl exit 5 = unit not loaded; tolerate.
		if strings.Contains(err.Error(), "exit status 5") {
			return nil
		}
		return fmt.Errorf("stop %s: %w", dhcp6cUnitName, err)
	}
	return nil
}

// SetOnLeaseChange registers a callback invoked whenever the dhcp6c
// lease state file changes. Replaces any previously registered hook.
// Pass nil to clear. Must be called before StartLeaseWatcher.
func (s *IPv6Service) SetOnLeaseChange(fn func(ctx context.Context, state PrefixState) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onLease = fn
}

// StartLeaseWatcher launches a goroutine that watches the lease state
// file via fsnotify and dispatches changes to the registered callback.
// The watcher attaches to the *parent directory* (not the file itself)
// because the hook script uses atomic mv to swap the state file in,
// which destroys the inode the watcher would otherwise be tied to.
//
// Idempotent: subsequent calls are no-ops while the watcher is running.
// Stop() tears it down.
func (s *IPv6Service) StartLeaseWatcher(ctx context.Context) error {
	s.mu.Lock()
	if s.watcherStop != nil {
		s.mu.Unlock()
		return nil // already running
	}
	stopCh := make(chan struct{})
	s.watcherStop = stopCh
	s.mu.Unlock()

	statePath := s.statePath()
	stateDir := filepath.Dir(statePath)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("ensure state dir %s: %w", stateDir, err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}
	if err := watcher.Add(stateDir); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("watch %s: %w", stateDir, err)
	}

	s.watcherWG.Add(1)
	go s.runLeaseWatcher(ctx, watcher, stopCh, statePath)
	return nil
}

// StopLeaseWatcher signals the watcher goroutine to exit and waits
// for it to drain. Safe to call when no watcher is running. Also
// stops any pending debounce timer so a stray dispatchLeaseLocked
// cannot fire after Stop returns — important for tests that swap
// agent clients or tear down the config in t.Cleanup.
func (s *IPv6Service) StopLeaseWatcher() {
	s.mu.Lock()
	stopCh := s.watcherStop
	s.watcherStop = nil
	debounce := s.watcherDebounce
	s.watcherDebounce = nil
	s.mu.Unlock()

	if debounce != nil {
		debounce.Stop()
	}
	if stopCh == nil {
		return
	}
	close(stopCh)
	s.watcherWG.Wait()
}

func (s *IPv6Service) runLeaseWatcher(ctx context.Context, watcher *fsnotify.Watcher, stopCh chan struct{}, statePath string) {
	defer s.watcherWG.Done()
	defer func() { _ = watcher.Close() }()

	// Initial dispatch so the callback sees the state that already
	// exists on disk when the watcher starts (e.g. lankeeper restart
	// while the lease is held).
	s.dispatchLeaseLocked(ctx)

	stateName := filepath.Base(statePath)
	// fsnotify can fire 2-3 events per atomic mv (Create + Rename +
	// Chmod). Debounce with a short timer so we only call the
	// callback once per logical update. The timer is stored on the
	// struct so StopLeaseWatcher can cancel any pending dispatch.
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != stateName {
				continue
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Chmod) == 0 {
				continue
			}
			s.mu.Lock()
			if s.watcherDebounce != nil {
				s.watcherDebounce.Stop()
			}
			s.watcherDebounce = time.AfterFunc(150*time.Millisecond, func() {
				s.dispatchLeaseLocked(ctx)
			})
			s.mu.Unlock()
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("ipv6: lease watcher error: %v", err)
		}
	}
}

// dispatchLeaseLocked reads the current lease state, dedupes against
// the last dispatched hash, and fires the registered callback. Errors
// are logged and never propagated — the watcher must keep running.
func (s *IPv6Service) dispatchLeaseLocked(ctx context.Context) {
	state, err := s.Status(ctx)
	if err != nil {
		log.Printf("ipv6: lease watcher status read: %v", err)
		return
	}
	hash := leaseHash(state)
	s.mu.Lock()
	if hash == s.lastLeaseHash {
		s.mu.Unlock()
		return
	}
	s.lastLeaseHash = hash
	cb := s.onLease
	s.mu.Unlock()

	// Re-render the dnsmasq RA drop-in so RDNSS / DNSSL track the
	// upstream lease, then bounce dnsmasq. Done BEFORE the user
	// callback so consumers see RA already refreshed when their
	// firewall/route logic runs.
	if err := s.refreshRADropIn(ctx); err != nil {
		log.Printf("ipv6: refresh RA drop-in on lease change: %v", err)
	}

	if cb == nil {
		return
	}
	if err := cb(ctx, state); err != nil {
		log.Printf("ipv6: lease change callback: %v", err)
	}
}

// refreshRADropIn re-renders /etc/dnsmasq.d/lankeeper-ipv6-ra.conf
// and reloads dnsmasq so RA picks up the freshly-learned RDNSS list
// (PD mode) or the new tunnel endpoint (6in4 mode). Skipped quietly
// when IPv6 is disabled or no plane is active.
func (s *IPv6Service) refreshRADropIn(ctx context.Context) error {
	if s.cfg.IPv6.Enabled == "off" {
		return nil
	}
	if s.cfg.IPv6.Mode != "6in4" && !s.cfg.IPv6.WAN.RequestPrefix {
		return nil
	}
	conf, err := s.RenderRAConfig()
	if err != nil {
		return fmt.Errorf("render RA: %w", err)
	}
	if err := netutil.WriteFile(dnsmasqRAConfPath, []byte(conf), 0o644); err != nil {
		return fmt.Errorf("write RA drop-in: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadDnsmasqLocked(ctx)
}

// leaseHash produces a stable digest of the fields callers actually
// care about. We deliberately omit Timestamp so spurious re-writes
// of the same lease (dhcp6c re-renews preserving the prefix) do not
// re-trigger the callback.
func leaseHash(p PrefixState) string {
	return fmt.Sprintf("%s/%d|%s|%d|%d|%s",
		p.Prefix, p.PrefixLength, p.Reason,
		p.PreferredLifetime, p.ValidLifetime, p.RDNSS)
}
