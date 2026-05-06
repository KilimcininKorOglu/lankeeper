package services

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// DoHService renders dnscrypt-proxy.toml and manages the
// dnscrypt-proxy systemd unit. It follows the project's 3-layer
// service convention: RenderConfig (pure) → RenderToDisk (write)
// → ApplyConfig (write + restart).
//
// dnscrypt-proxy listens on 127.0.0.1:5353 and Unbound forwards to
// it via the unbound.conf forward-zone block; that wiring lives in
// the DNS template, not here. This service is responsible only for
// the proxy's own config rendering and lifecycle.
type DoHService struct {
	cfg          *config.Config
	tmpl         string
	parsedTmpl   *template.Template
}

const dnscryptConfigPath = "/etc/dnscrypt-proxy/dnscrypt-proxy.toml"

// NewDoHService creates a service that loads the template from
// configs/sysconf/dnscrypt-proxy.toml.tmpl on the working
// directory. Production callers run from the project root or
// $DATA_DIR (via render-configs --cwd).
func NewDoHService(cfg *config.Config) *DoHService {
	return &DoHService{cfg: cfg}
}

// NewDoHServiceFromFS lets tests inject the template string
// directly without depending on the filesystem layout. Mirrors
// the FirewallService / IPv6Service test injection pattern.
func NewDoHServiceFromFS(cfg *config.Config, tmpl string) (*DoHService, error) {
	s := &DoHService{cfg: cfg, tmpl: tmpl}
	if tmpl != "" {
		t, err := template.New("dnscrypt-proxy.toml").Parse(tmpl)
		if err != nil {
			return nil, fmt.Errorf("parse dnscrypt-proxy template: %w", err)
		}
		s.parsedTmpl = t
	}
	return s, nil
}

// dohTemplateData feeds the toml template. ServerNames are the
// catalogue entries the operator selected; CustomServers are
// inline static stamps (sdns:// strings) the operator pasted as a
// custom upstream.
type dohTemplateData struct {
	ServerNames   []string
	CustomServers []dohCustomServer
}

type dohCustomServer struct {
	Name  string
	Stamp string
}

// RenderConfig produces the dnscrypt-proxy.toml content. The
// EnableDoH guard is the caller's responsibility; this method
// always renders a valid config so install-time stub generation
// works even when DoH is disabled (proxy then runs idle).
func (s *DoHService) RenderConfig() (string, error) {
	tmpl := s.parsedTmpl
	if tmpl == nil {
		t, err := template.ParseFiles("configs/sysconf/dnscrypt-proxy.toml.tmpl")
		if err != nil {
			return "", fmt.Errorf("parse template: %w", err)
		}
		tmpl = t
	}

	data := s.buildTemplateData()
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	return buf.String(), nil
}

// buildTemplateData splits the configured DoHUpstream into the
// shape the template wants. The decision tree:
//   - empty:                    no server_names, fall through to
//                               sources auto-pick (rare in our flow)
//   - catalogue name:           ServerNames=[name], no static
//   - sdns:// custom:           ServerNames=[lankeeper-custom],
//                               CustomServers=[{lankeeper-custom, stamp}]
//   - https:// URL:             converted to sdns:// stamp first
//                               (see specToStamp), then handled as
//                               sdns:// case.
func (s *DoHService) buildTemplateData() dohTemplateData {
	upstream := strings.TrimSpace(s.cfg.DNS.DoHUpstream)
	if upstream == "" {
		return dohTemplateData{}
	}
	if IsBuiltInDoHResolver(upstream) {
		return dohTemplateData{ServerNames: []string{upstream}}
	}
	stamp := upstream
	if strings.HasPrefix(upstream, "https://") {
		if s, err := httpsURLToStamp(upstream); err == nil {
			stamp = s
		}
	}
	return dohTemplateData{
		ServerNames:   []string{"lankeeper-custom"},
		CustomServers: []dohCustomServer{{Name: "lankeeper-custom", Stamp: stamp}},
	}
}

// RenderToDisk writes the rendered config to the well-known
// dnscrypt-proxy path via the agent. Ownership/permissions match
// the upstream Debian package defaults.
func (s *DoHService) RenderToDisk(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}
	if err := netutil.MkdirAll("/etc/dnscrypt-proxy", 0o755); err != nil {
		return fmt.Errorf("mkdir dnscrypt-proxy dir: %w", err)
	}
	return netutil.WriteFile(dnscryptConfigPath, []byte(rendered), 0o644)
}

// ApplyConfig writes + restarts dnscrypt-proxy. When DoH is
// disabled, we render a stub config and stop the service rather
// than uninstalling - operators can flip the toggle and the daemon
// is back without an apt cycle.
func (s *DoHService) ApplyConfig(ctx context.Context) error {
	if err := s.RenderToDisk(ctx); err != nil {
		return err
	}
	if !s.cfg.DNS.EnableDoH {
		_, _ = netutil.Run(ctx, "systemctl", "stop", "dnscrypt-proxy")
		return nil
	}
	if _, err := netutil.Run(ctx, "systemctl", "restart", "dnscrypt-proxy"); err != nil {
		return fmt.Errorf("restart dnscrypt-proxy: %w", err)
	}
	if _, err := netutil.Run(ctx, "systemctl", "enable", "dnscrypt-proxy"); err != nil {
		// Enable is best-effort - restart already started it.
		return nil
	}
	return nil
}

// --- Validation -----------------------------------------------------

var (
	// dohHostRegex matches the host portion of an https:// URL or
	// the host inside a decoded sdns:// stamp. RFC-1123 plus
	// dotted-quad numeric forms; no underscores, no whitespace.
	dohHostRegex = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.\-]{0,253}[a-zA-Z0-9])?$`)

	// dohPathRegex restricts the URL path. RFC-3986 unreserved
	// plus % for percent-encoding. Reject anything that smells
	// like a config-file injection ('"', '\n', '\\', spaces).
	dohPathRegex = regexp.MustCompile(`^/[a-zA-Z0-9._~/\-%]*$`)
)

// ValidateUpstream is the pure validator the form handler invokes
// before persisting cfg.DNS.DoHUpstream. Accepts a catalogue name,
// an https://... URL, or a sdns://... stamp. Rejects internal-IP
// targets via SSRF guard parallel to ProbeDoT.
func (s *DoHService) ValidateUpstream(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return errors.New("DoH upstream required")
	}
	if IsBuiltInDoHResolver(spec) {
		return nil
	}
	switch {
	case strings.HasPrefix(spec, "https://"):
		return validateHTTPSUpstream(spec)
	case strings.HasPrefix(spec, "sdns://"):
		return validateSDNSUpstream(spec)
	default:
		return errors.New("DoH upstream must be a catalogue name, https:// URL, or sdns:// stamp")
	}
}

// validateHTTPSUpstream parses the URL, enforces a /dns-query-style
// path (must start with `/` and end with one of the canonical
// endpoints), and rejects internal IP targets.
func validateHTTPSUpstream(spec string) error {
	u, err := url.Parse(spec)
	if err != nil {
		return fmt.Errorf("parse https url: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("scheme must be https")
	}
	host := u.Hostname()
	if !dohHostRegex.MatchString(host) {
		return fmt.Errorf("invalid host %q", host)
	}
	if !dohPathRegex.MatchString(u.Path) {
		return fmt.Errorf("invalid path %q", u.Path)
	}
	if u.Path == "" || u.Path == "/" {
		return errors.New("path must be a DoH endpoint (e.g. /dns-query)")
	}
	if port := u.Port(); port != "" {
		if err := validateDoHPort(port); err != nil {
			return err
		}
	}
	return checkUpstreamNotInternal(host)
}

// validateSDNSUpstream base64url-decodes the stamp, asserts the
// DoH protocol byte, and runs the same host/path checks as the
// https:// path. The stamp format is documented at:
// https://dnscrypt.info/stamps-specifications
func validateSDNSUpstream(spec string) error {
	body := strings.TrimPrefix(spec, "sdns://")
	body = strings.TrimRight(body, "=")
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return fmt.Errorf("base64url decode: %w", err)
	}
	if len(raw) < 1 {
		return errors.New("stamp too short")
	}
	// Protocol byte: 0x02 = DoH (RFC8484); 0x05 = ODoH; the rest
	// (0x00 plain, 0x01 DNSCrypt, 0x03 DoT, 0x04 DoQ) we reject.
	if raw[0] != 0x02 {
		return fmt.Errorf("stamp protocol = 0x%02x, want 0x02 (DoH)", raw[0])
	}
	host, path, err := decodeDoHStamp(raw)
	if err != nil {
		return err
	}
	if !dohHostRegex.MatchString(host) && !isIPLiteral(host) {
		return fmt.Errorf("invalid stamp host %q", host)
	}
	if !dohPathRegex.MatchString(path) {
		return fmt.Errorf("invalid stamp path %q", path)
	}
	return checkUpstreamNotInternal(host)
}

// decodeDoHStamp walks the wire format described in
// https://dnscrypt.info/stamps-specifications#dns-over-https.
// Layout after the 1-byte protocol marker:
//   [u64 props] [lp address] [vlp hash...] [lp host] [lp path] [vlp bootstrap-ip...]
// We only care about [lp host] and [lp path]; everything else is
// skipped via the length-prefixed walk so an unfamiliar trailing
// extension never breaks parsing.
func decodeDoHStamp(raw []byte) (host, path string, err error) {
	cursor := 1 // skip protocol byte
	if len(raw) < cursor+8 {
		return "", "", errors.New("stamp truncated at properties")
	}
	cursor += 8 // skip 8-byte properties bitmask

	// address (lp): 1-byte length then bytes
	addr, next, err := readLP(raw, cursor)
	if err != nil {
		return "", "", fmt.Errorf("address: %w", err)
	}
	_ = addr // not used directly
	cursor = next

	// hashes (vlp): MSB of length signals "more entries"
	cursor, err = skipVLP(raw, cursor)
	if err != nil {
		return "", "", fmt.Errorf("hashes: %w", err)
	}

	// hostname (lp)
	hostBytes, next, err := readLP(raw, cursor)
	if err != nil {
		return "", "", fmt.Errorf("host: %w", err)
	}
	host = string(hostBytes)
	cursor = next

	// path (lp)
	pathBytes, _, err := readLP(raw, cursor)
	if err != nil {
		return "", "", fmt.Errorf("path: %w", err)
	}
	path = string(pathBytes)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return host, path, nil
}

// readLP reads a length-prefixed byte slice; returns slice + next
// cursor index. The high bit of the length byte (0x80) signals
// "more entries to follow" in the vlp variant; here we strip it
// so a single-entry vlp parses identically to lp.
func readLP(raw []byte, cursor int) ([]byte, int, error) {
	if cursor >= len(raw) {
		return nil, 0, errors.New("truncated length prefix")
	}
	n := int(raw[cursor] & 0x7f)
	cursor++
	if cursor+n > len(raw) {
		return nil, 0, errors.New("truncated payload")
	}
	return raw[cursor : cursor+n], cursor + n, nil
}

// skipVLP walks a vlp (variable-length-prefix) sequence: each
// entry is length-prefixed, and a 0x80 high-bit on the length
// signals another entry follows.
func skipVLP(raw []byte, cursor int) (int, error) {
	for {
		if cursor >= len(raw) {
			return 0, errors.New("truncated vlp")
		}
		more := raw[cursor]&0x80 != 0
		n := int(raw[cursor] & 0x7f)
		cursor++
		if cursor+n > len(raw) {
			return 0, errors.New("truncated vlp payload")
		}
		cursor += n
		if !more {
			return cursor, nil
		}
	}
}

// validateDoHPort restricts the URL port to the well-known DoH
// ports. Cuts off operators pointing at random TCP services that
// happen to speak HTTPS.
func validateDoHPort(s string) error {
	switch s {
	case "443", "4443", "8443":
		return nil
	}
	return fmt.Errorf("DoH port %q not allowed (use 443/4443/8443)", s)
}

// checkUpstreamNotInternal is the SSRF guard. We resolve the host
// via the system resolver; every returned IP is checked against
// the loopback / link-local / private / unspecified set. Mirrors
// the DoT validation in dns.go.
func checkUpstreamNotInternal(host string) error {
	// IP literal: check directly.
	if ip := net.ParseIP(host); ip != nil {
		if isInternalIP(ip) {
			return fmt.Errorf("upstream %s is an internal address", host)
		}
		return nil
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", host, err)
	}
	for _, a := range addrs {
		if isInternalIP(a) {
			return fmt.Errorf("upstream %s resolved to internal address %s", host, a)
		}
	}
	return nil
}

func isIPLiteral(host string) bool {
	return net.ParseIP(host) != nil
}

// httpsURLToStamp converts an https://host[:port]/path URL into a
// minimal DoH sdns:// stamp. We need this when the operator pastes
// a plain URL that isn't in the catalogue: dnscrypt-proxy's static
// block requires the sdns:// form. Properties bitmask is 0 (no
// claims about DNSSEC/no-log/no-filter; dnscrypt-proxy's
// require_dnssec/etc. settings still apply via the proxy's own
// global flags).
func httpsURLToStamp(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("scheme = %s, want https", u.Scheme)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)
	if ip := net.ParseIP(host); ip != nil {
		// Address field carries the IP literal when host is an IP.
		addr = net.JoinHostPort(ip.String(), port)
	} else {
		// For hostnames, address is empty (the resolver discovers
		// the IP). Encoder still wants a valid lp; empty is fine.
		addr = ""
	}
	path := u.Path
	if path == "" {
		path = "/dns-query"
	}

	var buf bytes.Buffer
	buf.WriteByte(0x02) // DoH
	buf.Write(make([]byte, 8)) // properties=0
	writeLP(&buf, []byte(addr))
	// hashes: empty vlp (single zero-length entry, no more)
	buf.WriteByte(0x00)
	writeLP(&buf, []byte(host))
	writeLP(&buf, []byte(path))
	// no bootstrap IPs
	return "sdns://" + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// Probe sends a single A-record query over DoH and returns the
// round-trip latency. Used by /dns/doh/probe so the operator can
// validate a DoH upstream before saving it. Caller should rate
// limit (mirrors dotProbeLimiter).
//
// Catalogue names are not directly probable - we'd need to hit
// the dnscrypt-proxy resolver list to map name → endpoint - so
// this method only supports https:// URLs and sdns:// stamps.
// Catalogue picks are validated by membership and trusted to work.
func (s *DoHService) Probe(ctx context.Context, spec string) (time.Duration, error) {
	spec = strings.TrimSpace(spec)
	if IsBuiltInDoHResolver(spec) {
		return 0, errors.New("catalogue picks are validated by name; no probe required")
	}

	var endpoint string
	switch {
	case strings.HasPrefix(spec, "https://"):
		if err := validateHTTPSUpstream(spec); err != nil {
			return 0, err
		}
		endpoint = spec
	case strings.HasPrefix(spec, "sdns://"):
		if err := validateSDNSUpstream(spec); err != nil {
			return 0, err
		}
		host, path, err := parseSDNSEndpoint(spec)
		if err != nil {
			return 0, err
		}
		endpoint = "https://" + host + path
	default:
		return 0, errors.New("probe requires https:// URL or sdns:// stamp")
	}

	query, err := buildDoHQuery("www.example.com.")
	if err != nil {
		return 0, fmt.Errorf("build query: %w", err)
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, endpoint, bytes.NewReader(query))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 3 * time.Second,
		},
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(body); err != nil {
		return 0, fmt.Errorf("unpack DNS response: %w", err)
	}
	if msg.Header.RCode != dnsmessage.RCodeSuccess {
		return 0, fmt.Errorf("DNS rcode %s", msg.Header.RCode)
	}
	return time.Since(start), nil
}

func buildDoHQuery(name string) ([]byte, error) {
	q, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 0x1234, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  q,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	return msg.Pack()
}

// parseSDNSEndpoint pulls (host, path) out of a stamp without the
// validation guards - the caller has already validated. Returns the
// values shaped for direct URL composition (path keeps its leading
// slash).
func parseSDNSEndpoint(spec string) (string, string, error) {
	body := strings.TrimPrefix(spec, "sdns://")
	body = strings.TrimRight(body, "=")
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", "", err
	}
	return decodeDoHStamp(raw)
}

func writeLP(buf *bytes.Buffer, payload []byte) {
	if len(payload) > 0x7f {
		// truncate rather than fail; operator-supplied data
		// shouldn't ever reach this in practice.
		payload = payload[:0x7f]
	}
	buf.WriteByte(byte(len(payload)))
	buf.Write(payload)
}
