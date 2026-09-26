package services

import (
	"context"
	// Imported for a non-cryptographic counter id only; see
	// counterID for why collision resistance does not apply.
	// #nosec G505
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// MaxQoSClients caps the number of MAC addresses tracked by the
// per-client bandwidth sampler. Each client adds two nftables rules
// in the dedicated lankeeper_qos table; capping protects against
// unbounded growth on networks with rotating MAC addresses.
const MaxQoSClients = 64

const (
	qosTableName = "lankeeper_qos"
	qosChainName = "fwd"
	qosTmpPath   = "/tmp/lankeeper-qos.nft"
	qosRingSize  = 60
)

// ClientUsage is one snapshot of an individual MAC's traffic.
// InBytes/OutBytes are cumulative since the counter was created;
// InBPS/OutBPS are computed as the rate over the previous sample
// interval and are zero on the first sample.
type ClientUsage struct {
	MAC      string    `json:"mac"`
	IP       string    `json:"ip,omitempty"`
	Hostname string    `json:"hostname,omitempty"`
	InBytes  uint64    `json:"inBytes"`
	OutBytes uint64    `json:"outBytes"`
	InBPS    uint64    `json:"inBps"`
	OutBPS   uint64    `json:"outBps"`
	Updated  time.Time `json:"updated"`
}

// nftCounter mirrors a single counter object inside the nftables
// JSON output. Only the fields we care about are decoded; nft adds
// optional fields (handle, comment, etc.) that we ignore.
type nftCounter struct {
	Family  string `json:"family"`
	Table   string `json:"table"`
	Name    string `json:"name"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

type nftJSONEnvelope struct {
	Nftables []json.RawMessage `json:"nftables"`
}

// counterID returns the deterministic 8-hex-char identifier used in
// nftables counter names for a given MAC. Lower-cased and stripped
// of separators so two leases with different formatting end up with
// the same counter.
func counterID(mac string) string {
	norm := strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(mac))
	// Not cryptographic. The MAC is hashed only to derive an
	// 8-character nftables counter id.
	// #nosec G401
	sum := sha1.Sum([]byte(norm))
	return hex.EncodeToString(sum[:4])
}

// counterNames returns the in/out counter names for a MAC.
func counterNames(mac string) (inName, outName string) {
	id := counterID(mac)
	return "cli_" + id + "_in", "cli_" + id + "_out"
}

// renderQoSTable emits the nftables ruleset that the qos sampler
// owns. The forward chain hooks in at priority -200 so packets are
// seen before the firewall's filter chain (priority 0) drops them,
// but the table is kept independent so a flush of one does not
// affect the other.
//
// Download is counted on the client's leased IPv4 address. In the
// forward hook the link-layer header is the frame as it was received, so
// a download arrives with the WAN side's addressing (and a PPPoE WAN has
// no Ethernet header at all); an ether daddr match never saw a byte.
// Upload keeps ether saddr, which is the client's own frame on the LAN.
func renderQoSTable(macs []string, ips map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n", qosTableName)
	for _, mac := range macs {
		inName, outName := counterNames(mac)
		fmt.Fprintf(&b, "\tcounter %s { }\n", inName)
		fmt.Fprintf(&b, "\tcounter %s { }\n", outName)
	}
	fmt.Fprintf(&b, "\tchain %s {\n", qosChainName)
	b.WriteString("\t\ttype filter hook forward priority -200; policy accept;\n")
	for _, mac := range macs {
		inName, outName := counterNames(mac)
		if ip := net.ParseIP(ips[mac]).To4(); ip != nil {
			fmt.Fprintf(&b, "\t\tip daddr %s counter name %s\n", ip, inName)
		}
		fmt.Fprintf(&b, "\t\tether saddr %s counter name %s\n", strings.ToLower(mac), outName)
	}
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String()
}

// dedupAndCap normalises MAC casing, deduplicates, sorts for stable
// rendering, and applies the MaxQoSClients ceiling.
func dedupAndCap(macs []string) []string {
	seen := make(map[string]struct{}, len(macs))
	out := make([]string, 0, len(macs))
	for _, m := range macs {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	sort.Strings(out)
	if len(out) > MaxQoSClients {
		out = out[:MaxQoSClients]
	}
	return out
}

// applyClientCounters atomically replaces the lankeeper_qos table
// with one counter pair per MAC. Empty input flushes the table.
func (s *QoSService) applyClientCounters(ctx context.Context, ips map[string]string) error {
	macs := make([]string, 0, len(ips))
	for mac := range ips {
		macs = append(macs, mac)
	}
	macs = dedupAndCap(macs)

	var script strings.Builder
	fmt.Fprintf(&script, "table inet %s\ndelete table inet %s\n", qosTableName, qosTableName)
	if len(macs) > 0 {
		script.WriteString(renderQoSTable(macs, ips))
	}

	if err := netutil.WriteFile(qosTmpPath, []byte(script.String()), 0o600); err != nil {
		return fmt.Errorf("write qos nft script: %w", err)
	}
	if _, err := netutil.Run(ctx, "nft", "-f", qosTmpPath); err != nil {
		return fmt.Errorf("apply qos nft script: %w", err)
	}
	return nil
}

// RebuildClientCounters refreshes the lankeeper_qos table from the
// supplied lease snapshot. Idempotent: callers may invoke it on
// every lease change or on a periodic resync without checking
// whether anything actually changed.
func (s *QoSService) RebuildClientCounters(ctx context.Context, leases []Lease) error {
	ips := make(map[string]string, len(leases))
	for _, l := range leases {
		if l.MAC == "" {
			continue
		}
		ips[strings.ToLower(strings.TrimSpace(l.MAC))] = l.IP
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyClientCounters(ctx, ips); err != nil {
		return err
	}
	s.clientLeases = make(map[string]Lease, len(leases))
	for _, l := range leases {
		if l.MAC == "" {
			continue
		}
		s.clientLeases[strings.ToLower(l.MAC)] = l
	}
	return nil
}

// parseQoSCounters decodes the JSON output of
// `nft -j list table inet lankeeper_qos`. Counters whose names do
// not follow the cli_<id>_in / cli_<id>_out scheme are ignored so
// stray entries from manual nft sessions cannot corrupt the sample.
func parseQoSCounters(raw []byte) (map[string]nftCounter, error) {
	if len(raw) == 0 {
		return map[string]nftCounter{}, nil
	}
	var env nftJSONEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode nft json: %w", err)
	}
	out := make(map[string]nftCounter, len(env.Nftables))
	for _, item := range env.Nftables {
		var holder struct {
			Counter *nftCounter `json:"counter"`
		}
		if err := json.Unmarshal(item, &holder); err != nil {
			continue
		}
		if holder.Counter == nil {
			continue
		}
		c := *holder.Counter
		if !strings.HasPrefix(c.Name, "cli_") {
			continue
		}
		out[c.Name] = c
	}
	return out, nil
}

// SamplePerClient runs `nft -j list table inet lankeeper_qos`,
// computes deltas against the previously stored counters, and
// returns a fresh ClientUsage slice ordered by MAC. Hostname/IP
// are filled from the lease snapshot captured by the most recent
// RebuildClientCounters call.
func (s *QoSService) SamplePerClient(ctx context.Context) ([]ClientUsage, error) {
	out, err := netutil.RunSimple(ctx, "nft", "-j", "list", "table", "inet", qosTableName)
	if err != nil {
		// Table not yet created — return empty rather than failing
		// the sampler loop.
		if isMissingTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	counters, err := parseQoSCounters([]byte(out))
	if err != nil {
		return nil, err
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	usages := s.clientUsagesLocked(counters, s.sampleIntervalLocked(now), now)
	sort.Slice(usages, func(i, j int) bool { return usages[i].MAC < usages[j].MAC })

	s.storeCountersLocked(usages, counters)
	s.lastSample = now
	s.appendHistoryLocked(usages)
	return usages, nil
}

// isMissingTableError reports whether nft failed because the QoS table
// does not exist yet.
func isMissingTableError(err error) bool {
	return strings.Contains(err.Error(), "No such file or directory") ||
		strings.Contains(err.Error(), "does not exist")
}

// sampleIntervalLocked returns the seconds since the previous sample,
// at least 1. Callers must already hold s.mu.
func (s *QoSService) sampleIntervalLocked(now time.Time) float64 {
	if s.lastSample.IsZero() {
		s.lastSample = now
	}
	intervalSec := now.Sub(s.lastSample).Seconds()
	if intervalSec <= 0 {
		intervalSec = 1
	}
	return intervalSec
}

// clientUsagesLocked builds one usage entry per leased client. The rate
// stays zero for a client without a previous sample. Callers must
// already hold s.mu.
func (s *QoSService) clientUsagesLocked(counters map[string]nftCounter, intervalSec float64, now time.Time) []ClientUsage {
	usages := make([]ClientUsage, 0, len(s.clientLeases))
	for mac, lease := range s.clientLeases {
		inName, outName := counterNames(mac)
		cIn := counters[inName]
		cOut := counters[outName]

		var inBPS, outBPS uint64
		if prev, hadPrev := s.lastCounters[mac]; hadPrev {
			inBPS = bpsDelta(cIn.Bytes, prev.in, intervalSec)
			outBPS = bpsDelta(cOut.Bytes, prev.out, intervalSec)
		}

		usages = append(usages, ClientUsage{
			MAC:      mac,
			IP:       lease.IP,
			Hostname: lease.Hostname,
			InBytes:  cIn.Bytes,
			OutBytes: cOut.Bytes,
			InBPS:    inBPS,
			OutBPS:   outBPS,
			Updated:  now,
		})
	}
	return usages
}

// storeCountersLocked replaces the previous-counter cache with this
// sample's counters. Callers must already hold s.mu.
func (s *QoSService) storeCountersLocked(usages []ClientUsage, counters map[string]nftCounter) {
	if s.lastCounters == nil {
		s.lastCounters = make(map[string]counterPair, len(usages))
	}
	clear(s.lastCounters)
	for _, u := range usages {
		inName, outName := counterNames(u.MAC)
		s.lastCounters[u.MAC] = counterPair{
			in:  counters[inName].Bytes,
			out: counters[outName].Bytes,
		}
	}
}

// bpsDelta is a helper that protects against counter resets (where
// the new value is smaller than the previous one) by returning zero
// rather than a wrap-around value.
func bpsDelta(curr, prev uint64, intervalSec float64) uint64 {
	if curr < prev || intervalSec <= 0 {
		return 0
	}
	delta := curr - prev
	bps := float64(delta*8) / intervalSec
	return uint64(bps)
}

// appendHistoryLocked pushes one sample into the per-MAC ring
// buffer. Callers must already hold s.mu.
func (s *QoSService) appendHistoryLocked(usages []ClientUsage) {
	if s.history == nil {
		s.history = make(map[string][]ClientUsage, len(usages))
	}
	seen := make(map[string]struct{}, len(usages))
	for _, u := range usages {
		seen[u.MAC] = struct{}{}
		buf := s.history[u.MAC]
		if len(buf) >= qosRingSize {
			buf = buf[len(buf)-qosRingSize+1:]
		}
		buf = append(buf, u)
		s.history[u.MAC] = buf
	}
	for mac := range s.history {
		if _, ok := seen[mac]; !ok {
			delete(s.history, mac)
		}
	}
}

// ClientHistory returns the ring buffer for a single MAC. Empty
// slice for unknown MACs. The returned slice is a copy; callers may
// mutate it freely.
func (s *QoSService) ClientHistory(mac string) []ClientUsage {
	mac = strings.ToLower(mac)
	s.mu.RLock()
	defer s.mu.RUnlock()
	src, ok := s.history[mac]
	if !ok {
		return nil
	}
	out := make([]ClientUsage, len(src))
	copy(out, src)
	return out
}

// StartClientSampler launches the periodic sampler. It re-syncs the
// counter table from the supplied lease provider every resyncEvery
// ticks, samples every interval, and publishes ClientUsage slices
// to the broker as event "qos-clients". Stops when ctx is done.
//
// wg may be nil. When supplied, the goroutine is counted into it so
// shutdown can wait for it to drain.
func (s *QoSService) StartClientSampler(
	ctx context.Context,
	publisher Publisher,
	leaseProvider func() ([]Lease, error),
	interval time.Duration,
	resyncEvery int,
	wg *sync.WaitGroup,
) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if resyncEvery <= 0 {
		resyncEvery = 30
	}

	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		s.runClientSampler(ctx, publisher, leaseProvider, interval, resyncEvery)
	}()
}

// runClientSampler is the sampler loop. It returns when ctx is done.
func (s *QoSService) runClientSampler(
	ctx context.Context,
	publisher Publisher,
	leaseProvider func() ([]Lease, error),
	interval time.Duration,
	resyncEvery int,
) {
	t := time.NewTicker(interval)
	defer t.Stop()

	// Initial rebuild so the UI has counters on the first SSE tick
	// instead of waiting resyncEvery ticks.
	s.rebuildFromLeases(ctx, leaseProvider, "initial")

	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick++
			if tick%resyncEvery == 0 {
				s.rebuildFromLeases(ctx, leaseProvider, "periodic")
			}
			s.sampleAndPublish(ctx, publisher)
		}
	}
}

// rebuildFromLeases re-syncs the counter table from the current leases.
// Failures are logged: the sampler keeps running on the old table.
func (s *QoSService) rebuildFromLeases(ctx context.Context, leaseProvider func() ([]Lease, error), stage string) {
	leases, err := leaseProvider()
	if err != nil {
		log.Printf("qos sampler: %s lease read: %v", stage, err)
		return
	}
	if err := s.RebuildClientCounters(ctx, leases); err != nil {
		log.Printf("qos sampler: %s rebuild: %v", stage, err)
	}
}

// sampleAndPublish takes one sample and publishes it when a publisher is
// set.
func (s *QoSService) sampleAndPublish(ctx context.Context, publisher Publisher) {
	usages, err := s.SamplePerClient(ctx)
	if err != nil {
		log.Printf("qos sampler: sample: %v", err)
		return
	}
	if publisher != nil {
		publisher.Publish("qos-clients", usages)
	}
}

// Publisher is the minimal slice of *web.SSEBroker that the qos
// sampler depends on. Defined here to keep services free of any
// import cycle into internal/web.
type Publisher interface {
	Publish(event string, data any)
}

// counterPair is the previous-sample byte counts kept around so
// SamplePerClient can compute deltas.
type counterPair struct {
	in, out uint64
}

// QoSService internal mutable state used by the per-client sampler
// is declared on the struct itself in qos.go. lastCounters keeps
// cumulative bytes from the previous sample, lastSample keeps the
// previous sample timestamp, clientLeases is the snapshot from the
// last RebuildClientCounters call, and history is the per-MAC ring
// buffer of recent ClientUsage samples. All four are guarded by
// s.mu.
