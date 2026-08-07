package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// Default device name used when cfg.IPv6.Tunnel.Device is empty. The
// "lkt" prefix avoids collisions with operators who already run a
// stock he-ipv6 systemd unit on the same box.
const defaultSixInFourDevice = "lkt6in4"

// PPPoE-clamped MTU for 6in4 over PPPoE: 1500 - 20 (outer IPv4) - 28
// (PPPoE) = 1452. Direct WAN gets 1480 (1500 - 20).
const (
	sixInFourMTUDirect = 1480
	sixInFourMTUPPPoE  = 1452
)

// HE.net DDNS endpoint. Only HTTPS is supported per HE forum docs.
const heNicUpdateURL = "https://ipv4.tunnelbroker.net/nic/update"

// sixInFourStateRelPath is the on-disk record of the last successful
// Apply. Lives under /var/lib/lankeeper/state/ which is already on
// the agent write whitelist.
const sixInFourStateRelPath = "state/ipv6-tunnel.json"

// TunnelStatus is the observable state of the local 6in4 tunnel.
type TunnelStatus struct {
	Active       bool      `json:"active"`
	Device       string    `json:"device"`
	ServerIPv4   string    `json:"serverIPv4"`
	ClientIPv6   string    `json:"clientIPv6"`
	RoutedPrefix string    `json:"routedPrefix"`
	MTU          int       `json:"mtu"`
	LocalIPv4    string    `json:"localIPv4"`
	LastApplied  time.Time `json:"lastApplied"`
	LastDDNS     string    `json:"lastDDNS,omitempty"`
	LastDDNSTime time.Time `json:"lastDDNSTime"`
	RxBytes      uint64    `json:"rxBytes,omitempty"`
	TxBytes      uint64    `json:"txBytes,omitempty"`
}

// SixInFourService manages the local 6in4 tunnel termination plus
// the HE.net /nic/update DDNS client. The IPv4 endpoint is volatile
// (PPPoE rotates it on every reconnect) so this service is wired to
// PPPoE.SetOnConnect/SetOnDisconnect in web/server.go.
type SixInFourService struct {
	cfg        *config.Config
	httpClient *http.Client
	mu         sync.Mutex
	running    bool
	// lastIPv4 caches the address pushed to /nic/update so we can
	// dedupe identical updates and avoid HE's "abuse" rate limiter.
	lastIPv4 string
	// statePath is overridable for tests; production uses the
	// computed path under /var/lib/lankeeper/state/.
	statePathOverride string
	// localIPv4Override lets tests skip the live interface lookup
	// (which depends on the host's actual ppp0/eth0 device names
	// and is unavailable on macOS / inside CI sandboxes).
	localIPv4Override string
}

// NewSixInFourService builds the service with sane defaults: 10s
// HTTP timeout for /nic/update, no proxy.
func NewSixInFourService(cfg *config.Config) *SixInFourService {
	return &SixInFourService{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SetStatePathForTest overrides the state file location. Call before
// any Start/Stop/UpdateRemoteIPv4 so tests redirect writes into a
// TempDir.
func (s *SixInFourService) SetStatePathForTest(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statePathOverride = path
}

// SetHTTPClientForTest swaps the HTTP client. Used by httptest-based
// DDNS tests so the service hits the test server instead of HE.net.
func (s *SixInFourService) SetHTTPClientForTest(c *http.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.httpClient = c
}

// SetLocalIPv4ForTest pins the WAN-side IPv4 address that Start uses
// as the tunnel's local endpoint. Bypasses GetInterfaceAddresses so
// tests run without a live ppp0/eth0 device.
func (s *SixInFourService) SetLocalIPv4ForTest(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.localIPv4Override = ip
}

func (s *SixInFourService) statePath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statePathOverride != "" {
		return s.statePathOverride
	}
	return "/var/lib/lankeeper/" + sixInFourStateRelPath
}

// device returns the configured tunnel interface name, falling back
// to defaultSixInFourDevice.
func (s *SixInFourService) device() string {
	if d := strings.TrimSpace(s.cfg.IPv6.Tunnel.Device); d != "" {
		return d
	}
	return defaultSixInFourDevice
}

// effectiveMTU clamps the MTU to PPPoE's 1452 when PPPoE is the
// underlying WAN, otherwise 1480 (RFC 4213 baseline).
func (s *SixInFourService) effectiveMTU() int {
	if s.cfg.PPPoE.Username != "" {
		return sixInFourMTUPPPoE
	}
	return sixInFourMTUDirect
}

// validate reports the first missing config field. Called by
// Start/Restart so an operator who saved a half-filled tunnel form
// gets a clear error instead of a half-built sit interface.
func (s *SixInFourService) validate() error {
	t := s.cfg.IPv6.Tunnel
	if t.ServerIPv4 == "" {
		return fmt.Errorf("6in4: ServerIPv4 not configured")
	}
	if t.ClientIPv6 == "" {
		return fmt.Errorf("6in4: ClientIPv6 not configured")
	}
	if t.RoutedPrefix == "" {
		return fmt.Errorf("6in4: RoutedPrefix not configured")
	}
	return nil
}

// localIPv4 picks the WAN-side IPv4 the tunnel should attach to.
// PPPoE uses ppp0; otherwise the first Role:wan interface's address
// is read straight off the kernel via netutil.GetInterfaceAddresses.
// Tests can pin the value via SetLocalIPv4ForTest.
func (s *SixInFourService) localIPv4() (string, error) {
	s.mu.Lock()
	override := s.localIPv4Override
	s.mu.Unlock()
	if override != "" {
		return override, nil
	}
	dev := "ppp0"
	if s.cfg.PPPoE.Username == "" {
		for _, ifc := range s.cfg.Interfaces {
			if ifc.Role == "wan" {
				dev = ifc.Device
				break
			}
		}
	}
	addrs, err := netutil.GetInterfaceAddresses(dev)
	if err != nil {
		return "", fmt.Errorf("read %s addresses: %w", dev, err)
	}
	for _, a := range addrs {
		// Skip IPv6 entries; we want the v4 endpoint.
		if !strings.Contains(a, ":") && strings.Contains(a, ".") {
			return strings.SplitN(a, "/", 2)[0], nil
		}
	}
	return "", fmt.Errorf("no IPv4 address on %s", dev)
}

// Start brings the tunnel up. Idempotent: a running tunnel is torn
// down and rebuilt so config edits take effect without a service
// restart. Errors at any step abort and leave the partial state in
// place — Stop() can clean up.
func (s *SixInFourService) Start(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	localV4, err := s.localIPv4()
	if err != nil {
		return err
	}

	dev := s.device()
	mtu := s.effectiveMTU()

	// Best-effort cleanup of a stale interface from a previous run.
	// "Cannot find device" is exit 1 on iproute2; tolerate it.
	_, _ = netutil.Run(ctx, "ip", "tunnel", "del", dev)

	if _, err := netutil.Run(ctx, "ip", "tunnel", "add", dev,
		"mode", "sit",
		"remote", s.cfg.IPv6.Tunnel.ServerIPv4,
		"local", localV4,
		"ttl", "255"); err != nil {
		return fmt.Errorf("ip tunnel add: %w", err)
	}
	if _, err := netutil.Run(ctx, "ip", "link", "set", dev,
		"up", "mtu", fmt.Sprintf("%d", mtu)); err != nil {
		return fmt.Errorf("ip link set up: %w", err)
	}
	if _, err := netutil.Run(ctx, "ip", "addr", "add",
		s.cfg.IPv6.Tunnel.ClientIPv6, "dev", dev); err != nil {
		return fmt.Errorf("ip addr add: %w", err)
	}
	if _, err := netutil.Run(ctx, "ip", "-6", "route", "add",
		"::/0", "dev", dev); err != nil {
		return fmt.Errorf("ip -6 route add: %w", err)
	}

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	if err := s.persistState(TunnelStatus{
		Active:       true,
		Device:       dev,
		ServerIPv4:   s.cfg.IPv6.Tunnel.ServerIPv4,
		ClientIPv6:   s.cfg.IPv6.Tunnel.ClientIPv6,
		RoutedPrefix: s.cfg.IPv6.Tunnel.RoutedPrefix,
		MTU:          mtu,
		LocalIPv4:    localV4,
		LastApplied:  time.Now(),
	}); err != nil {
		log.Printf("6in4: persist state: %v", err)
	}

	return nil
}

// Stop tears the tunnel down in strict reverse order. All steps are
// best-effort because Stop is also called from teardown paths where
// the kernel may already have removed the interface.
func (s *SixInFourService) Stop(ctx context.Context) error {
	dev := s.device()

	_, _ = netutil.Run(ctx, "ip", "-6", "route", "del", "::/0", "dev", dev)
	_, _ = netutil.Run(ctx, "ip", "link", "set", dev, "down")
	_, _ = netutil.Run(ctx, "ip", "tunnel", "del", dev)

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()

	if err := s.persistState(TunnelStatus{Active: false, Device: dev}); err != nil {
		log.Printf("6in4: persist state on stop: %v", err)
	}
	return nil
}

// Restart is Stop+Start. Used by the PPPoE on-connect callback: every
// PPPoE reconnect rotates the IPv4 endpoint, which must propagate to
// the kernel sit driver too.
func (s *SixInFourService) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start(ctx)
}

// wanted reports whether the tunnel should exist at all, given the
// operator's settings. Both conditions matter: the plane is selected by
// Mode, but IPv6 as a whole can be switched off without touching it.
func (s *SixInFourService) wanted() bool {
	return s.cfg.IPv6.Enabled != "off" && s.cfg.IPv6.Mode == "6in4"
}

// ApplyConfig converges the tunnel to the configured state, mirroring
// IPv6Service.ApplyConfig for the prefix-delegation plane.
//
// Start and stop used to be decided at each call site, keyed on Mode
// alone, so turning IPv6 off while the mode stayed at 6in4 skipped the
// teardown (the mode had not changed) and still ran the bring-up. The
// sit interface and default route survived, and every WAN reconnect
// rebuilt them. Deciding here means the handler and both PPPoE hooks
// cannot disagree about it.
func (s *SixInFourService) ApplyConfig(ctx context.Context) error {
	if !s.wanted() {
		return s.Stop(ctx)
	}
	return s.Restart(ctx)
}

// IsRunning reports whether the last Apply succeeded. Cheap accessor
// for the /ipv6 status card.
func (s *SixInFourService) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Status returns the persisted tunnel state, augmented with live
// RX/TX byte counters when the device exists. Missing state file is
// not an error — returns zero TunnelStatus.
func (s *SixInFourService) Status(ctx context.Context) (TunnelStatus, error) {
	var st TunnelStatus
	raw, err := netutil.ReadFile(s.statePath())
	if err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	if st.Device == "" {
		st.Device = s.device()
	}
	if st.RoutedPrefix == "" {
		st.RoutedPrefix = s.cfg.IPv6.Tunnel.RoutedPrefix
	}

	// Augment with live counters when the interface is up.
	if rx, tx, ok := readSitCounters(ctx, st.Device); ok {
		st.RxBytes = rx
		st.TxBytes = tx
	}
	return st, nil
}

// readSitCounters reads RX/TX byte counts via `ip -s -j link show`.
// JSON output is iproute2 4.x+; on older kernels the fallback returns
// !ok and the caller leaves the counters at zero.
func readSitCounters(ctx context.Context, dev string) (uint64, uint64, bool) {
	out, err := netutil.RunSimple(ctx, "ip", "-s", "-j", "link", "show", dev)
	if err != nil || out == "" {
		return 0, 0, false
	}
	type stats64 struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	}
	type entry struct {
		Stats64 stats64 `json:"stats64"`
	}
	var entries []entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) == 0 {
		return 0, 0, false
	}
	return entries[0].Stats64.RxBytes, entries[0].Stats64.TxBytes, true
}

// persistState writes the tunnel state JSON atomically via the agent.
// netutil.WriteFile already does mkdir-parent + atomic rename inside
// the agent so callers don't need to.
func (s *SixInFourService) persistState(st TunnelStatus) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return netutil.WriteFile(s.statePath(), raw, 0o644)
}

// DDNSResult is the parsed reply from /nic/update.
type DDNSResult struct {
	Code string // "good" | "nochg" | "badauth" | "abuse" | other
	IP   string // present on "good"/"nochg"
	Raw  string
}

// UpdateRemoteIPv4 informs HE.net that our IPv4 endpoint changed and
// returns the parsed Dyn-DNS-style response. Deduped against
// lastIPv4 so a no-op reconnect does not burn the rate limiter.
// AutoUpdate=false in config short-circuits the call.
func (s *SixInFourService) UpdateRemoteIPv4(ctx context.Context, currentIPv4 string) (DDNSResult, error) {
	if currentIPv4 == "" {
		return DDNSResult{}, fmt.Errorf("UpdateRemoteIPv4: empty IPv4")
	}

	s.mu.Lock()
	if currentIPv4 == s.lastIPv4 {
		s.mu.Unlock()
		return DDNSResult{Code: "nochg", IP: currentIPv4, Raw: "dedup"}, nil
	}
	t := s.cfg.IPv6.Tunnel
	client := s.httpClient
	s.mu.Unlock()

	if t.TunnelID == "" || t.Username == "" || t.UpdateKey == "" {
		return DDNSResult{}, fmt.Errorf("UpdateRemoteIPv4: TunnelID/Username/UpdateKey not configured")
	}

	q := url.Values{}
	q.Set("hostname", t.TunnelID)
	q.Set("myip", currentIPv4)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		heNicUpdateURL+"?"+q.Encode(), nil)
	if err != nil {
		return DDNSResult{}, fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(t.Username, t.UpdateKey)

	resp, err := client.Do(req)
	if err != nil {
		return DDNSResult{}, fmt.Errorf("nic/update: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return DDNSResult{}, fmt.Errorf("read body: %w", err)
	}
	res := parseDDNSResponse(string(body))

	// Cache the IP only on a positive response code so a transient
	// badauth doesn't pin lastIPv4 against the next retry.
	if res.Code == "good" || res.Code == "nochg" {
		s.mu.Lock()
		s.lastIPv4 = currentIPv4
		s.mu.Unlock()
	}

	// Reflect the result in the persisted status.
	if st, _ := s.Status(ctx); st.Device != "" {
		st.LastDDNS = res.Code
		st.LastDDNSTime = time.Now()
		_ = s.persistState(st)
	}

	return res, nil
}

// parseDDNSResponse splits the Dyn-DNS-format body into Code+IP.
// Examples: "good 1.2.3.4", "nochg 1.2.3.4", "badauth", "abuse".
func parseDDNSResponse(body string) DDNSResult {
	body = strings.TrimSpace(body)
	res := DDNSResult{Raw: body}
	if body == "" {
		return res
	}
	fields := strings.Fields(body)
	res.Code = fields[0]
	if len(fields) > 1 {
		res.IP = fields[1]
	}
	return res
}
