package services

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// loopbackListener returns a listener and a counter of accepted
// connections, so a dial that should have been refused is visible as an
// accept rather than only as a missing error.
func loopbackListener(t *testing.T) (net.Listener, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln, &accepted
}

// TestDoHProbeDialsThroughTheInternalAddressGuard is the regression
// test. The upstream hostname was validated with a single lookup, then
// the transport resolved again on its own at connect time, so the
// address that was checked was not necessarily the one dialled. A record
// repointed between the two, or a domain its owner simply points inward,
// had the router open outbound TLS into its own LAN or localhost.
//
// The dial is exercised directly because a divergence between the two
// lookups cannot be staged from a unit test. What is verifiable, and
// what was missing, is that the probe's own dialer refuses an internal
// destination at connect time.
func TestDoHProbeDialsThroughTheInternalAddressGuard(t *testing.T) {
	ln, accepted := loopbackListener(t)

	tr, ok := newDoHProbeClient().Transport.(*http.Transport)
	if !ok {
		t.Fatal("the probe client no longer uses an *http.Transport")
	}
	if tr.DialContext == nil {
		t.Fatal("the probe transport has no DialContext, so it resolves and dials unguarded")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := tr.DialContext(ctx, "tcp", ln.Addr().String())
	if err == nil {
		_ = conn.Close()
		t.Fatal("the probe dialled a loopback address")
	}
	if !strings.Contains(err.Error(), "internal address") {
		t.Errorf("expected the guard to refuse it, got: %v", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener accepted %d connections; none may be made", n)
	}
}

// TestDoTProbeDialsThroughTheInternalAddressGuard covers the same gap on
// the DoT side, where the spec's host may equally be a name.
func TestDoTProbeDialsThroughTheInternalAddressGuard(t *testing.T) {
	ln, accepted := loopbackListener(t)

	dialer := newDoTProbeDialer("dns.example")
	if dialer.NetDialer == nil || dialer.NetDialer.Control == nil {
		t.Fatal("the DoT probe dialer has no address guard")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := dialer.NetDialer.DialContext(ctx, "tcp", ln.Addr().String())
	if err == nil {
		_ = conn.Close()
		t.Fatal("the probe dialled a loopback address")
	}
	if !strings.Contains(err.Error(), "internal address") {
		t.Errorf("expected the guard to refuse it, got: %v", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener accepted %d connections; none may be made", n)
	}
}

// TestDoTProbeKeepsItsTLSContract makes sure the extraction did not drop
// the SNI or the minimum version the probe relies on to detect a MITM.
func TestDoTProbeKeepsItsTLSContract(t *testing.T) {
	dialer := newDoTProbeDialer("dns.quad9.net")
	if dialer.Config == nil {
		t.Fatal("no TLS config")
	}
	if dialer.Config.ServerName != "dns.quad9.net" {
		t.Errorf("ServerName = %q, want the SNI passed in", dialer.Config.ServerName)
	}
	if dialer.Config.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want TLS 1.2", dialer.Config.MinVersion)
	}
}

// TestProbeGuardAllowsAPublicAddress keeps the guard from refusing the
// destinations a probe exists for.
func TestProbeGuardAllowsAPublicAddress(t *testing.T) {
	for _, addr := range []string{"1.1.1.1:853", "9.9.9.9:443", "[2606:4700::1111]:443"} {
		if err := refuseInternalAddress("tcp", addr, nil); err != nil {
			t.Errorf("public address %s refused: %v", addr, err)
		}
	}
}
