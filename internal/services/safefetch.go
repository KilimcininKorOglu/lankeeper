package services

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// This file holds the outbound HTTP clients every fetch-a-URL feature
// must use. This process sits between the LAN and the internet, so a
// request it can be talked into making reaches the router's own
// loopback services and every LAN, VLAN and VPN segment behind it.
//
// The address check runs in the dialer's Control hook rather than as a
// pre-flight resolve of the URL host. That placement matters: the hook
// sees the address the connection is actually about to use, so it also
// covers a redirect to an internal host and a name that resolves to a
// public address when checked and an internal one when dialled.

var (
	// outboundFetchClient is for bulk downloads (blocklists, playlists),
	// which are routinely several megabytes over a slow link.
	outboundFetchClient = newGuardedClient(2 * time.Minute)

	// outboundProbeClient is for liveness probes, which must fail fast
	// so a dead target does not stall the health check loop.
	outboundProbeClient = newGuardedClient(5 * time.Second)
)

// newGuardedClient builds an HTTP client that refuses internal
// destinations. No proxy is configured on purpose: a proxy would move
// the connection to the proxy's address and the guard would inspect
// that instead of the real destination.
func newGuardedClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   refuseInternalAddress,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   2,
		},
	}
}

// refuseInternalAddress is the dialer Control hook. By the time it runs
// the resolver has finished, so address is an IP literal with a port.
func refuseInternalAddress(network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("refusing to dial network %q", network)
	}

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("dial address %q is not an IP address", address)
	}
	if isInternalIP(ip) {
		return fmt.Errorf("refusing to connect to internal address %s", ip)
	}
	return nil
}

// maxFetchBytes bounds a downloaded blocklist or playlist. Both are
// parsed line by line into an in-memory slice, so a body that never
// ends, or simply one far larger than the operator expected, would grow
// this process until the appliance runs out of memory. The ceiling sits
// well above any real file: a hosts-format blocklist this size holds
// roughly a million entries.
const maxFetchBytes = 32 << 20

// limitedBody caps how much of a response a parser can consume and
// remembers whether the server still had more to send. A truncated
// blocklist or playlist must be reported, not quietly accepted: half a
// blocklist looks like a working one.
type limitedBody struct {
	r io.Reader
	n int64
}

func newLimitedBody(r io.Reader) *limitedBody {
	// One byte past the cap, so reaching it proves there was more.
	return &limitedBody{r: io.LimitReader(r, maxFetchBytes+1)}
}

func (l *limitedBody) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	return n, err
}

func (l *limitedBody) overflowed() bool {
	return l.n > maxFetchBytes
}

// errFetchTooLarge is returned when a response exceeds maxFetchBytes.
var errFetchTooLarge = fmt.Errorf("response exceeds the %d byte limit", maxFetchBytes)

// validateOutboundURL rejects the schemes the transport should never be
// asked to handle. The destination itself is checked at dial time.
func validateOutboundURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must use http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL %q has no host", raw)
	}
	return nil
}
