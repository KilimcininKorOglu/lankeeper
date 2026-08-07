package services

import (
	"context"
	// Imported for a non-cryptographic label hash only; see the
	// call sites for why collision resistance does not apply.
	// #nosec G505
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// MetricsService composes a single read-only snapshot drawn from
// every domain service that owns numeric runtime state. Its only state
// is the snapshot cache; calling Snapshot() concurrently is safe (each
// contributing service guards its own data, and the cache has its own
// lock).
//
// We deliberately avoid the github.com/prometheus/client_golang
// dependency: the exposition format is ~50 LOC of stdlib fprintf,
// and the project tracks a tight 5-direct-dep budget.
type MetricsService struct {
	cfg     *config.Config
	monitor *MonitorService
	dns     *DNSService
	dhcp    *DHCPService
	qos     *QoSService
	vpn     *VPNService
	backup  *BackupService
	update  *UpdateService
	openvpn *OpenVPNService

	// Snapshot collection is cached because /metrics carries no
	// authentication. Collecting live meant every scrape forked several
	// privileged subprocesses through the root agent, and the agent
	// serialises all traffic behind one mutex-guarded connection, so a
	// sustained scrape from any LAN device delayed the operator's own
	// privileged operations. The lock also coalesces concurrent
	// scrapes: the first collects, the rest read what it produced.
	cacheMu  sync.Mutex
	cached   MetricsSnapshot
	cachedAt time.Time
	cacheTTL time.Duration
}

// metricsCacheTTL bounds how often collection runs. Prometheus scrapes
// at 15 s by default, so a shorter window keeps a normal scrape fresh
// while capping the privileged work an abusive one can cause.
const metricsCacheTTL = 10 * time.Second

// NewMetricsService takes nil-safe references; the snapshot
// gracefully degrades when any contributor is missing (handy for
// tests that only exercise a subset).
func NewMetricsService(
	cfg *config.Config,
	monitor *MonitorService,
	dns *DNSService,
	dhcp *DHCPService,
	qos *QoSService,
	vpn *VPNService,
	backup *BackupService,
	update *UpdateService,
	openvpn *OpenVPNService,
) *MetricsService {
	return &MetricsService{
		cfg:      cfg,
		monitor:  monitor,
		dns:      dns,
		dhcp:     dhcp,
		qos:      qos,
		vpn:      vpn,
		backup:   backup,
		update:   update,
		openvpn:  openvpn,
		cacheTTL: metricsCacheTTL,
	}
}

// MetricsSnapshot is a flat union of all the numeric state we
// expose. Sub-collectors live in metrics_collectors.go; the writer
// in metrics_exposition.go projects this struct onto the Prometheus
// text format.
type MetricsSnapshot struct {
	BuildVersion string
	BuildCommit  string

	UptimeSeconds float64
	CPUPercent    float64
	MemoryTotal   uint64
	MemoryUsed    uint64
	Temperature   float64

	Interfaces []IfaceMetric

	DHCPLeases int

	DNSQueriesTotal     uint64
	DNSCacheHitsTotal   uint64
	DNSCacheMissesTotal uint64
	DNSBlockedTotal     uint64

	Clients []ClientBandwidthMetric

	WGPeers      []WGPeerMetric
	S2SPeers     []S2SPeerMetric
	OpenVPNPeers int

	BackupLastRunUnix  int64
	BackupLastStatusOK int
	BackupHistorySize  int

	PPPoEConnected int
	IPv6Active     int
	IPv6Mode       string
	FirewallActive int
}

// IfaceMetric captures cumulative byte counters per OS interface.
type IfaceMetric struct {
	Device  string
	RxBytes uint64
	TxBytes uint64
}

// ClientBandwidthMetric is one row of per-MAC bandwidth state. The MAC
// is hashed for stable label values, and no hostname is carried: this
// row is written to an unauthenticated endpoint, so a human-readable
// device name would let any LAN device enumerate its neighbours by name
// and watch their traffic. The hashed MAC alone identifies the series;
// the operator sees real hostnames on the authenticated QoS page.
type ClientBandwidthMetric struct {
	MAC     string
	RxBytes uint64
	TxBytes uint64
	RxBPS   uint64
	TxBPS   uint64
}

// WGPeerMetric captures one WireGuard server peer (the road-warrior
// kind). HandshakeAge is in seconds; -1 means "never handshaken".
type WGPeerMetric struct {
	Name         string
	HandshakeAge int64
	Online       int
	RxBytes      uint64
	TxBytes      uint64
}

// S2SPeerMetric mirrors WGPeerMetric for site-to-site peers; kept
// separate so dashboards can split road-warrior vs branch-router
// tunnels along their semantic axis.
type S2SPeerMetric struct {
	Name         string
	HandshakeAge int64
	Online       int
	RxBytes      uint64
	TxBytes      uint64
}

// Snapshot returns the metric set, collecting only when the cached one
// has aged out. The returned value shares its slices with the cache, so
// callers must treat it as read-only; the exposition writer does.
func (s *MetricsService) Snapshot(ctx context.Context) MetricsSnapshot {
	if s == nil {
		return MetricsSnapshot{}
	}

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	ttl := s.cacheTTL
	if ttl <= 0 {
		ttl = metricsCacheTTL
	}
	if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < ttl {
		return s.cached
	}

	snap := s.collect(ctx)
	s.cached = snap
	s.cachedAt = time.Now()
	return snap
}

// collect composes the full metric set. Errors from individual
// collectors are swallowed (logged at most by the collector itself)
// so a single failed sub-system can't take the whole scrape down.
func (s *MetricsService) collect(ctx context.Context) MetricsSnapshot {
	snap := MetricsSnapshot{}
	if s.update != nil {
		v := s.update.GetVersionInfo()
		snap.BuildVersion = v.Version
		snap.BuildCommit = v.Commit
	}
	if s.monitor != nil {
		cur := s.monitor.GetCurrent()
		snap.UptimeSeconds = cur.Uptime.Seconds()
		snap.CPUPercent = cur.CPUPercent
		snap.MemoryTotal = cur.RAMTotal
		snap.MemoryUsed = cur.RAMUsed
		snap.Temperature = cur.Temperature
		snap.Interfaces = ifaceMetricsFromMonitor(cur.Interfaces)
	}
	if s.dhcp != nil {
		if leases, err := s.dhcp.GetLeases(); err == nil {
			snap.DHCPLeases = len(leases)
		}
	}
	if s.dns != nil {
		if stats, err := s.dns.GetStats(ctx); err == nil && stats != nil {
			// Clamped rather than converted straight through.
			// The counts come from `unbound-control stats_noreset`
			// via Sscanf, which leaves the field untouched on a
			// malformed value and would happily scan a negative
			// one. uint64(-1) is 1.8e19, which Prometheus would
			// take as a real counter value and rate() would turn
			// into a spike no operator could explain.
			snap.DNSQueriesTotal = uint64(max(stats.TotalQueries, 0))
			snap.DNSCacheHitsTotal = uint64(max(stats.CacheHits, 0))
			snap.DNSCacheMissesTotal = uint64(max(stats.CacheMisses, 0))
			snap.DNSBlockedTotal = uint64(max(stats.BlockedCount, 0))
		}
	}
	if s.qos != nil {
		snap.Clients = s.collectClientMetrics()
	}
	if s.vpn != nil {
		snap.WGPeers, snap.S2SPeers = s.collectVPNMetrics(ctx)
	}
	if s.backup != nil {
		snap.BackupLastRunUnix = s.cfg.Backup.LastRun.Unix()
		if s.cfg.Backup.LastStatus == "ok" {
			snap.BackupLastStatusOK = 1
		}
		snap.BackupHistorySize = len(s.cfg.Backup.History)
	}
	if s.openvpn != nil {
		snap.OpenVPNPeers = s.openvpn.ActiveSessions(ctx)
	}
	snap.PPPoEConnected = pppoeConnectedFromCfg(s.cfg)
	snap.IPv6Active, snap.IPv6Mode = ipv6StateFromCfg(s.cfg)
	snap.FirewallActive = firewallActive(ctx)
	return snap
}

// ifaceMetricsFromMonitor flattens the monitor's per-iface map into
// a deterministic slice (Prometheus exposition prefers stable
// ordering for diffability).
func ifaceMetricsFromMonitor(m map[string]IfaceStats) []IfaceMetric {
	out := make([]IfaceMetric, 0, len(m))
	for dev, st := range m {
		out = append(out, IfaceMetric{
			Device:  dev,
			RxBytes: st.RxBytes,
			TxBytes: st.TxBytes,
		})
	}
	// Stable order by device name.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Device < out[i].Device {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// firewallActive runs `nft list ruleset` and reports 1 if the
// command succeeded with non-empty output. Cheap probe; the agent
// whitelist already covers nft.
func firewallActive(ctx context.Context) int {
	out, err := netutil.RunSimple(ctx, "nft", "list", "ruleset")
	if err != nil || strings.TrimSpace(out) == "" {
		return 0
	}
	return 1
}

// pppoeConnectedFromCfg infers PPPoE status from `cfg.PPPoE.Username`
// being set plus the presence of a `pppd` process. RunSimple's nil
// agent in tests degrades gracefully; we report 0 when we can't
// confirm.
func pppoeConnectedFromCfg(cfg *config.Config) int {
	if cfg == nil || cfg.PPPoE.Username == "" {
		return 0
	}
	out, err := netutil.RunSimple(context.Background(), "pgrep", "-x", "pppd")
	if err != nil || strings.TrimSpace(out) == "" {
		return 0
	}
	return 1
}

// ipv6StateFromCfg returns (active=1/0, mode_string). Mode keeps
// the cfg.IPv6.Mode value verbatim ("off"/"pd"/"6in4") so the
// info-style metric label round-trips exactly.
func ipv6StateFromCfg(cfg *config.Config) (int, string) {
	if cfg == nil {
		return 0, "off"
	}
	mode := cfg.IPv6.Mode
	if mode == "" {
		mode = "off"
	}
	if cfg.IPv6.Enabled == "off" || mode == "off" {
		return 0, mode
	}
	return 1, mode
}

// macHash is a stable shortener for MAC labels. Some hostnames are
// the only meaningful per-client identifier when the operator
// hasn't named devices; the hash keeps cardinality bounded even
// when MACs spoof.
func macHash(mac string) string {
	// Not cryptographic. The MAC is hashed only to give each
	// client a stable, non-identifying metric label and to bound
	// cardinality. Collision resistance is irrelevant here.
	// #nosec G401
	h := sha1.Sum([]byte(strings.ToLower(strings.ReplaceAll(mac, ":", ""))))
	return hex.EncodeToString(h[:4])
}

// peerHash shortens a VPN peer name for the exposition, the same way
// macHash does for LAN clients.
//
// The name is operator-chosen free text, so in practice a person's name
// or a remote site name, and it was previously emitted verbatim on six
// series covering presence, handshake age and traffic volume. That
// endpoint carries no authentication, and unlike a LAN client, a remote
// VPN peer is not otherwise observable from the local segment: there is
// no ARP or mDNS equivalent to fall back on, so the exposition was the
// disclosure. The hash keeps each peer a distinct, stable series
// without naming it.
func peerHash(name string) string {
	// Not cryptographic. The peer name is hashed so the
	// unauthenticated /metrics endpoint carries a stable series
	// key instead of an operator-assigned name.
	// #nosec G401
	h := sha1.Sum([]byte(name))
	return hex.EncodeToString(h[:4])
}
