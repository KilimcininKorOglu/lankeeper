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
func renderQoSTable(macs []string) string {
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
		fmt.Fprintf(&b, "\t\tether daddr %s counter name %s\n", strings.ToLower(mac), inName)
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
func (s *QoSService) applyClientCounters(ctx context.Context, macs []string) error {
	macs = dedupAndCap(macs)

	var script strings.Builder
	fmt.Fprintf(&script, "table inet %s\ndelete table inet %s\n", qosTableName, qosTableName)
	if len(macs) > 0 {
		script.WriteString(renderQoSTable(macs))
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
	macs := make([]string, 0, len(leases))
	for _, l := range leases {
		if l.MAC == "" {
			continue
		}
		macs = append(macs, l.MAC)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyClientCounters(ctx, macs); err != nil {
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
		if strings.Contains(err.Error(), "No such file or directory") ||
			strings.Contains(err.Error(), "does not exist") {
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

	if s.lastSample.IsZero() {
		s.lastSample = now
	}
	intervalSec := now.Sub(s.lastSample).Seconds()
	if intervalSec <= 0 {
		intervalSec = 1
	}

	usages := make([]ClientUsage, 0, len(s.clientLeases))
	for mac, lease := range s.clientLeases {
		inName, outName := counterNames(mac)
		cIn := counters[inName]
		cOut := counters[outName]

		prev, hadPrev := s.lastCounters[mac]
		var inBPS, outBPS uint64
		if hadPrev {
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

	sort.Slice(usages, func(i, j int) bool { return usages[i].MAC < usages[j].MAC })

	// Refresh the previous-counter cache + sample timestamp.
	if s.lastCounters == nil {
		s.lastCounters = make(map[string]counterPair, len(usages))
	}
	for k := range s.lastCounters {
		delete(s.lastCounters, k)
	}
	for _, u := range usages {
		inName, outName := counterNames(u.MAC)
		s.lastCounters[u.MAC] = counterPair{
			in:  counters[inName].Bytes,
			out: counters[outName].Bytes,
		}
	}
	s.lastSample = now
	s.appendHistoryLocked(usages)
	return usages, nil
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
		t := time.NewTicker(interval)
		defer t.Stop()

		// Initial rebuild + first sample so the UI has data on
		// the first SSE tick instead of waiting interval seconds.
		if leases, err := leaseProvider(); err == nil {
			if rerr := s.RebuildClientCounters(ctx, leases); rerr != nil {
				log.Printf("qos sampler: initial rebuild: %v", rerr)
			}
		}

		tick := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tick++
				if tick%resyncEvery == 0 {
					if leases, err := leaseProvider(); err == nil {
						if rerr := s.RebuildClientCounters(ctx, leases); rerr != nil {
							log.Printf("qos sampler: periodic rebuild: %v", rerr)
						}
					}
				}
				usages, err := s.SamplePerClient(ctx)
				if err != nil {
					log.Printf("qos sampler: sample: %v", err)
					continue
				}
				if publisher != nil {
					publisher.Publish("qos-clients", usages)
				}
			}
		}
	}()
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
