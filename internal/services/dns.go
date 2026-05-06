package services

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type DNSService struct {
	cfg       *config.Config
	mu        sync.RWMutex
	queryBuf  []QueryLogEntry
	bufSize   int
	stats     DNSStats
	cancel    context.CancelFunc
}

type DNSStats struct {
	TotalQueries  int
	CacheHits     int
	CacheMisses   int
	BlockedCount  int
	TopDomains    []DomainCount
	TopClients    []ClientCount
	TopBlocked    []DomainCount
}

type DomainCount struct {
	Domain string
	Count  int
}

type ClientCount struct {
	IP       string
	Hostname string
	Count    int
}

type QueryLogEntry struct {
	Timestamp time.Time
	ClientIP  string
	Domain    string
	QueryType string
	Status    string
	Blocked   bool
}

func NewDNSService(cfg *config.Config) *DNSService {
	bufSize := 10000
	return &DNSService{
		cfg:      cfg,
		queryBuf: make([]QueryLogEntry, 0, bufSize),
		bufSize:  bufSize,
	}
}

type unboundTemplateData struct {
	IPv6Enabled     bool
	AllowSubnets    []string
	ULAPrefix       string
	CacheSize       int
	QueryLogEnabled bool
	QueryLogPath    string
	EnableDoT       bool
	DoTUpstream     string
	StaticRecords   []renderStaticRecord
}

// renderStaticRecord is the template-facing view of a static DNS record:
// the persisted fields plus a pre-computed PTR string (empty for IPv6).
type renderStaticRecord struct {
	Name      string
	IP        string
	LocalZone bool
	PTR       string
}

// RenderConfig returns the rendered unbound.conf as a string. Pure
// computation — no I/O. Use RenderToDisk to write the result to /etc.
func (s *DNSService) RenderConfig() (string, error) {
	funcMap := template.FuncMap{
		"mul": func(a, b int) int { return a * b },
	}

	tmpl, err := template.New("unbound.conf.tmpl").Funcs(funcMap).ParseFiles("configs/sysconf/unbound.conf.tmpl")
	if err != nil {
		return "", fmt.Errorf("parse unbound template: %w", err)
	}

	data := unboundTemplateData{
		IPv6Enabled:     s.cfg.IPv6.Enabled != "off",
		CacheSize:       s.cfg.DNS.CacheSize,
		QueryLogEnabled: s.cfg.DNS.QueryLog.Enabled,
		QueryLogPath:    s.cfg.DNS.QueryLog.LogPath,
		EnableDoT:       s.cfg.DNS.EnableDoT,
		DoTUpstream:     s.cfg.DNS.DoTUpstream,
		ULAPrefix:       s.cfg.IPv6.LAN.ULA.Prefix,
		StaticRecords:   buildRenderStaticRecords(s.cfg.DNS.StaticRecords),
	}

	if data.QueryLogPath == "" {
		data.QueryLogPath = "/var/log/unbound-query.log"
	}

	if data.CacheSize == 0 {
		data.CacheSize = 64
	}

	for _, vlan := range s.cfg.VLANs {
		for _, iface := range s.cfg.Interfaces {
			if iface.ID == vlan.Parent && iface.Address != "" {
				data.AllowSubnets = append(data.AllowSubnets, subnetFromCIDR(iface.Address)+"/24")
			}
		}
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render unbound.conf: %w", err)
	}

	return buf.String(), nil
}

// RenderToDisk renders the unbound configuration to /etc/unbound/unbound.conf
// without reloading the service. Suitable for install-time invocation.
func (s *DNSService) RenderToDisk(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}
	return netutil.WriteFile("/etc/unbound/unbound.conf", []byte(rendered), 0o644)
}

// ApplyConfig renders to disk and reloads unbound. Use at runtime when the
// service is already up.
func (s *DNSService) ApplyConfig(ctx context.Context) error {
	if err := s.RenderToDisk(ctx); err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *DNSService) Reload(ctx context.Context) error {
	_, err := netutil.Run(ctx, "unbound-control", "reload")
	if err != nil {
		return fmt.Errorf("unbound reload: %w", err)
	}
	return nil
}

func (s *DNSService) GetStats(ctx context.Context) (*DNSStats, error) {
	out, err := netutil.RunSimple(ctx, "unbound-control", "stats_noreset")
	if err != nil {
		return nil, fmt.Errorf("unbound stats: %w", err)
	}

	stats := &DNSStats{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "total.num.queries":
			_, _ = fmt.Sscanf(val, "%d", &stats.TotalQueries)
		case "total.num.cachehits":
			_, _ = fmt.Sscanf(val, "%d", &stats.CacheHits)
		case "total.num.cachemiss":
			_, _ = fmt.Sscanf(val, "%d", &stats.CacheMisses)
		}
	}

	s.mu.RLock()
	stats.BlockedCount = s.stats.BlockedCount
	stats.TopDomains = s.stats.TopDomains
	stats.TopClients = s.stats.TopClients
	stats.TopBlocked = s.stats.TopBlocked
	s.mu.RUnlock()

	return stats, nil
}

func (s *DNSService) UpdateBlocklist(ctx context.Context) error {
	var allDomains []string

	for _, url := range s.cfg.DNS.BlocklistURLs {
		domains, err := downloadBlocklist(ctx, url)
		if err != nil {
			log.Printf("blocklist download failed %s: %v", url, err)
			continue
		}
		allDomains = append(allDomains, domains...)
	}

	var buf strings.Builder
	seen := make(map[string]bool, len(allDomains))
	for _, domain := range allDomains {
		if seen[domain] {
			continue
		}
		seen[domain] = true
		fmt.Fprintf(&buf, "local-zone: \"%s\" always_refuse\n", domain)
	}

	if err := netutil.WriteFile("/etc/unbound/blocklist.conf", []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("write blocklist: %w", err)
	}

	log.Printf("blocklist updated: %d domains", len(seen))
	return s.Reload(ctx)
}

func downloadBlocklist(ctx context.Context, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var domains []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == "0.0.0.0" || fields[0] == "127.0.0.1") {
			domain := fields[1]
			if domain != "localhost" && domain != "0.0.0.0" {
				domains = append(domains, domain)
			}
		}
	}

	return domains, scanner.Err()
}

var queryLogRegex = regexp.MustCompile(`\[(\d+)\]\s+\S+\s+info:\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)`)

func (s *DNSService) StartQueryLogTail(ctx context.Context) {
	if !s.cfg.DNS.QueryLog.Enabled {
		return
	}

	ctx, s.cancel = context.WithCancel(ctx)
	go s.tailQueryLog(ctx)
	go s.aggregateStats(ctx)
}

func (s *DNSService) StopQueryLogTail() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *DNSService) GetRecentQueries(limit, offset int) []QueryLogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.queryBuf)
	if offset >= total {
		return nil
	}

	end := total - offset
	start := end - limit
	if start < 0 {
		start = 0
	}

	result := make([]QueryLogEntry, end-start)
	copy(result, s.queryBuf[start:end])

	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return result
}

func (s *DNSService) ClearQueryLog(ctx context.Context) error {
	s.mu.Lock()
	s.queryBuf = s.queryBuf[:0]
	s.stats = DNSStats{}
	s.mu.Unlock()

	logPath := s.cfg.DNS.QueryLog.LogPath
	if logPath != "" {
		if err := os.Truncate(logPath, 0); err != nil && !os.IsNotExist(err) {
			log.Printf("dns clear log: truncate %s: %v", logPath, err)
		}
	}

	return nil
}

// GetStaticRecords returns the configured static DNS records.
func (s *DNSService) GetStaticRecords() []config.StaticDNSRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.DNS.StaticRecords
}

// GetDNSConfig exposes the live DNS config block for read-only handler use.
func (s *DNSService) GetDNSConfig() config.DNSConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.DNS
}

// ProbeDoT performs a one-shot TLS handshake + DNS query against the
// given upstream and returns the round-trip latency on success.
// Accepted upstream formats:
//   - "ip"                          (port 853, no SNI)
//   - "ip@port"                     (custom port, no SNI)
//   - "ip@port#hostname"            (custom port + SNI for cert validation)
//   - "ip#hostname"                 (port 853 + SNI)
// Probes are timeout-bounded (5s).
func (s *DNSService) ProbeDoT(ctx context.Context, upstream string) (time.Duration, error) {
	host, port, sni, err := parseAndValidateDoTSpec(strings.TrimSpace(upstream))
	if err != nil {
		return 0, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 3 * time.Second},
		Config: &tls.Config{
			// SNI is mandatory (validateDoTUpstream rejects empty);
			// crypto/tls verifies the cert chain AND VerifyHostname
			// against this name, defeating MITM attempts that present
			// a valid cert issued for some other domain.
			ServerName: sni,
			MinVersion: tls.VersionTLS12,
		},
	}
	addr := net.JoinHostPort(host, port)
	start := time.Now()
	conn, err := dialer.DialContext(probeCtx, "tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Send a tiny "cloudflare.com. A IN" query (any name works; we only
	// care that the upstream answers at all).
	var msg dnsmessage.Message
	msg.ID = 0x1234
	msg.RecursionDesired = true
	msg.Questions = []dnsmessage.Question{{
		Name:  dnsmessage.MustNewName("cloudflare.com."),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}}
	wire, err := msg.Pack()
	if err != nil {
		return 0, fmt.Errorf("pack: %w", err)
	}
	// DoT prefixes each message with a 2-byte length (RFC 7858).
	frame := make([]byte, 2+len(wire))
	frame[0] = byte(len(wire) >> 8)
	frame[1] = byte(len(wire))
	copy(frame[2:], wire)
	if _, err := conn.Write(frame); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}
	hdr := make([]byte, 2)
	if _, err := conn.Read(hdr); err != nil {
		return 0, fmt.Errorf("read header: %w", err)
	}
	respLen := int(hdr[0])<<8 | int(hdr[1])
	if respLen <= 0 || respLen > 65535 {
		return 0, fmt.Errorf("invalid response length")
	}
	resp := make([]byte, respLen)
	if _, err := conn.Read(resp); err != nil {
		return 0, fmt.Errorf("read body: %w", err)
	}
	var parsed dnsmessage.Message
	if err := parsed.Unpack(resp); err != nil {
		return 0, fmt.Errorf("unpack: %w", err)
	}
	return time.Since(start), nil
}

// parseDoTSpec splits an unbound-style DoT upstream string into its
// host, port, and SNI components. Defaults: port=853, sni="" (no
// validation hostname). Examples:
//   "1.1.1.1"                          → host=1.1.1.1, port=853, sni=""
//   "1.1.1.1@853"                      → host=1.1.1.1, port=853, sni=""
//   "1.1.1.1@853#cloudflare-dns.com"   → host=..., port=853, sni=cloudflare-dns.com
func parseDoTSpec(spec string) (host, port, sni string) {
	port = "853"
	if i := strings.Index(spec, "#"); i >= 0 {
		sni = spec[i+1:]
		spec = spec[:i]
	}
	if i := strings.Index(spec, "@"); i >= 0 {
		port = spec[i+1:]
		spec = spec[:i]
	}
	host = spec
	return
}

// parseAndValidateDoTSpec wraps parseDoTSpec, rejects upstreams that
// omit the `#hostname` SNI suffix, restricts the destination port to
// the IANA-assigned DoT port (853), and refuses host values that
// resolve to loopback / link-local / RFC-1918 / IMDS ranges so the
// probe cannot be coerced into a TCP port scanner against the router
// itself or the LAN. Without the SNI suffix Go's TLS stack performs
// only chain validation and silently MITMs every query; without
// IP-range guarding the probe acts as an SSRF oracle.
func parseAndValidateDoTSpec(spec string) (host, port, sni string, err error) {
	// Reject any character that could break out of the unbound.conf
	// `forward-addr:` line and inject a sibling directive. Real DoT
	// upstreams only need alphanumerics, dots, hyphens, `:` (IPv6),
	// `@` (port separator), and `#` (SNI separator). Whitespace,
	// newlines, NULs, `"`, and shell metacharacters are out.
	if !hasOnlyDoTSpecChars(spec) {
		return "", "", "", fmt.Errorf("DoT upstream contains characters that could inject unbound.conf directives")
	}
	host, port, sni = parseDoTSpec(spec)
	if host == "" {
		return "", "", "", fmt.Errorf("empty or invalid upstream")
	}
	if sni == "" {
		return "", "", "", fmt.Errorf("DoT upstream must include #hostname for certificate validation (e.g. %q#cloudflare-dns.com)", host)
	}
	if port != "853" {
		// IANA assigned DoT to 853 (RFC 7858). Free-form ports turn
		// the probe into an arbitrary TCP connector.
		return "", "", "", fmt.Errorf("DoT upstream port must be 853, got %q", port)
	}
	if err := validateDoTHostNotInternal(host); err != nil {
		return "", "", "", err
	}
	return host, port, sni, nil
}

// dotSpecChars is the conservative byte allowlist for the entire
// unbound DoT upstream string: alphanumerics, dots, hyphens, colon
// (for IPv6 literals), `@` (port separator), `#` (SNI separator).
const dotSpecChars = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"0123456789" +
	".-:@#"

func hasOnlyDoTSpecChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(dotSpecChars, rune(s[i])) {
			return false
		}
	}
	return true
}

// validateDoTHostNotInternal rejects DoT upstream hosts that point at
// the router itself or anywhere inside the LAN/cloud-metadata space.
// Accepts both literal IPs and hostnames; hostnames are resolved and
// every returned address must be public.
func validateDoTHostNotInternal(host string) error {
	var addrs []net.IP
	if ip := net.ParseIP(host); ip != nil {
		addrs = []net.IP{ip}
	} else {
		// Bound the resolver to the upstream system DNS — DoT probe
		// is operator-driven, sub-second resolves are normal.
		ips, lookupErr := net.LookupIP(host)
		if lookupErr != nil {
			return fmt.Errorf("resolve %q: %w", host, lookupErr)
		}
		if len(ips) == 0 {
			return fmt.Errorf("no addresses for %q", host)
		}
		addrs = ips
	}
	for _, ip := range addrs {
		if isInternalIP(ip) {
			return fmt.Errorf("DoT upstream %q resolves to a private/loopback/link-local address (%s); refusing to probe internal hosts", host, ip)
		}
	}
	return nil
}

// isInternalIP reports whether the address falls in any range we
// must not let an authenticated user probe via the DoT button:
// loopback (127.0.0.0/8, ::1), link-local (169.254.0.0/16, fe80::/10),
// unique-local (fc00::/7), and RFC-1918 private space.
func isInternalIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	// IsPrivate covers RFC-1918 + IPv6 ULA (fc00::/7).
	if ip.IsPrivate() {
		return true
	}
	return false
}

// SaveDNSSettings persists the DoT toggle and upstream string to
// router.yaml. Caller is expected to follow up with ApplyConfig so
// unbound reloads. When DoT is enabled the upstream MUST carry a
// `#hostname` SNI suffix so unbound (and our probe) can verify the
// server certificate against a name; otherwise the connection
// silently degrades to chain-only validation and is MITM-able.
func (s *DNSService) SaveDNSSettings(enableDoT bool, dotUpstream string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dotUpstream = strings.TrimSpace(dotUpstream)
	if enableDoT {
		if _, _, _, err := parseAndValidateDoTSpec(dotUpstream); err != nil {
			return err
		}
	}
	s.cfg.DNS.EnableDoT = enableDoT
	s.cfg.DNS.DoTUpstream = dotUpstream
	return s.cfg.SaveToFile()
}

// FindStaticRecordIndexBySource returns the slice index of the first
// record whose Source and Name match (case-insensitive on Name), or -1
// if no match. Used by automated callers (DHCP mirror) to remove
// records they own without disturbing user-added ones.
func (s *DNSService) FindStaticRecordIndexBySource(source, name string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, r := range s.cfg.DNS.StaticRecords {
		if r.Source == source && strings.EqualFold(r.Name, name) {
			return i
		}
	}
	return -1
}

// buildRenderStaticRecords expands persisted records with a pre-computed
// IPv4 PTR (empty when the IP is IPv6 or invalid, or when the record
// explicitly disables auto PTR).
func buildRenderStaticRecords(records []config.StaticDNSRecord) []renderStaticRecord {
	out := make([]renderStaticRecord, 0, len(records))
	for _, r := range records {
		ptr := ""
		if !r.DisableAutoPTR {
			ptr = ipv4PTR(r.IP)
		}
		out = append(out, renderStaticRecord{
			Name:      r.Name,
			IP:        r.IP,
			LocalZone: r.LocalZone,
			PTR:       ptr,
		})
	}
	return out
}

// ipv4PTR returns "<d>.<c>.<b>.<a>.in-addr.arpa." for an IPv4 dotted-quad
// or "" for IPv6 / invalid input.
func ipv4PTR(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	v4 := parsed.To4()
	if v4 == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", v4[3], v4[2], v4[1], v4[0])
}

// AddStaticRecord adds a forward A record. The Name should be an FQDN
// (e.g. "printer.hermes.lan"). LocalZone=true also emits a typetransparent
// local-zone for split-DNS.
func (s *DNSService) AddStaticRecord(rec config.StaticDNSRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.cfg.DNS.StaticRecords {
		if strings.EqualFold(r.Name, rec.Name) {
			return fmt.Errorf("DNS record %s already exists", rec.Name)
		}
	}
	s.cfg.DNS.StaticRecords = append(s.cfg.DNS.StaticRecords, rec)
	return s.cfg.SaveToFile()
}

// RemoveStaticRecord deletes the record at the given index.
func (s *DNSService) RemoveStaticRecord(index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.cfg.DNS.StaticRecords) {
		return fmt.Errorf("invalid static record index: %d", index)
	}
	s.cfg.DNS.StaticRecords = append(
		s.cfg.DNS.StaticRecords[:index],
		s.cfg.DNS.StaticRecords[index+1:]...,
	)
	return s.cfg.SaveToFile()
}

func (s *DNSService) tailQueryLog(ctx context.Context) {
	logPath := s.cfg.DNS.QueryLog.LogPath
	if logPath == "" {
		logPath = "/var/log/unbound/queries.log"
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		f, err := os.Open(logPath)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		_, _ = f.Seek(0, 2)

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				_ = f.Close()
				return
			default:
			}

			entry := parseQueryLogLine(scanner.Text())
			if entry == nil {
				continue
			}

			s.mu.Lock()
			if len(s.queryBuf) >= s.bufSize {
				s.queryBuf = s.queryBuf[1:]
			}
			s.queryBuf = append(s.queryBuf, *entry)
			if entry.Blocked {
				s.stats.BlockedCount++
			}
			s.mu.Unlock()
		}

		_ = f.Close()
		time.Sleep(1 * time.Second)
	}
}

func parseQueryLogLine(line string) *QueryLogEntry {
	matches := queryLogRegex.FindStringSubmatch(line)
	if matches == nil {
		return nil
	}

	var ts int64
	_, _ = fmt.Sscanf(matches[1], "%d", &ts)

	entry := &QueryLogEntry{
		Timestamp: time.Unix(ts, 0),
		ClientIP:  matches[2],
		Domain:    strings.TrimSuffix(matches[3], "."),
		QueryType: matches[4],
		Status:    "NOERROR",
	}

	if strings.Contains(line, "REFUSED") {
		entry.Status = "REFUSED"
		entry.Blocked = true
	} else if strings.Contains(line, "NXDOMAIN") {
		entry.Status = "NXDOMAIN"
	}

	return entry
}

func (s *DNSService) aggregateStats(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.computeTopLists()
		}
	}
}

func (s *DNSService) computeTopLists() {
	s.mu.Lock()
	defer s.mu.Unlock()

	domainCounts := make(map[string]int)
	clientCounts := make(map[string]int)
	blockedCounts := make(map[string]int)

	for _, q := range s.queryBuf {
		domainCounts[q.Domain]++
		clientCounts[q.ClientIP]++
		if q.Blocked {
			blockedCounts[q.Domain]++
		}
	}

	s.stats.TopDomains = topN(domainCounts, 10)
	s.stats.TopBlocked = topN(blockedCounts, 10)

	s.stats.TopClients = make([]ClientCount, 0, 10)
	for _, dc := range topN(clientCounts, 10) {
		s.stats.TopClients = append(s.stats.TopClients, ClientCount{
			IP:    dc.Domain,
			Count: dc.Count,
		})
	}
}

func topN(counts map[string]int, n int) []DomainCount {
	result := make([]DomainCount, 0, len(counts))
	for k, v := range counts {
		result = append(result, DomainCount{Domain: k, Count: v})
	}

	for i := 0; i < len(result) && i < n; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Count > result[i].Count {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	if len(result) > n {
		result = result[:n]
	}
	return result
}
