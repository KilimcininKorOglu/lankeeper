package services_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func TestNewDNSService(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewDNSService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestQueryLogEmpty(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewDNSService(cfg)

	queries := svc.GetRecentQueries(10, 0)
	if len(queries) != 0 {
		t.Errorf("expected 0 queries, got %d", len(queries))
	}
}

// TestSaveDNSSettingsRejectsBareIPDoTUpstream verifies that enabling
// DoT with a bare-IP upstream — i.e. one without a `#hostname` SNI
// suffix — is refused at the service boundary. Without SNI the TLS
// stack performs only chain validation and any CA-signed cert can
// MITM the resolver.
func TestSaveDNSSettingsRejectsBareIPDoTUpstream(t *testing.T) {
	cases := []struct {
		name     string
		upstream string
		wantErr  bool
	}{
		{"bare IP rejected", "1.1.1.1", true},
		{"bare IP with port rejected", "1.1.1.1@853", true},
		{"empty upstream rejected", "", true},
		{"IP + SNI accepted", "1.1.1.1#cloudflare-dns.com", false},
		{"IP + port + SNI accepted", "1.1.1.1@853#cloudflare-dns.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
			svc := services.NewDNSService(cfg)
			err := svc.SaveDNSSettings(true, tc.upstream)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for upstream %q, got nil", tc.upstream)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for upstream %q: %v", tc.upstream, err)
			}
			// Non-empty bare-IP upstreams must surface the #hostname
			// requirement explicitly so operators can fix the entry.
			// Empty upstreams hit the earlier "empty or invalid"
			// branch and are exempt from this assertion.
			if tc.wantErr && err != nil && tc.upstream != "" &&
				!strings.Contains(err.Error(), "#hostname") {
				t.Errorf("error should mention #hostname requirement, got %q", err.Error())
			}
		})
	}
}

// TestSaveDNSSettingsRejectsSSRFTargets verifies that DoT upstreams
// pointing at loopback / link-local / RFC-1918 / IMDS — or at any
// non-853 port — are refused at the service boundary. The probe
// would otherwise act as a TCP port scanner against the router
// itself and the LAN.
func TestSaveDNSSettingsRejectsSSRFTargets(t *testing.T) {
	cases := []struct {
		name     string
		upstream string
	}{
		{"loopback v4", "127.0.0.1#cloudflare-dns.com"},
		{"loopback v6", "::1#cloudflare-dns.com"},
		{"link-local v4 IMDS", "169.254.169.254#metadata"},
		{"RFC-1918 10/8", "10.10.10.1#lan"},
		{"RFC-1918 192.168/16", "192.168.1.1#lan"},
		{"RFC-1918 172.16/12", "172.16.0.1#lan"},
		{"IPv6 ULA", "fd00::1#lan"},
		{"non-853 port", "1.1.1.1@8443#cloudflare-dns.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
			svc := services.NewDNSService(cfg)
			if err := svc.SaveDNSSettings(true, tc.upstream); err == nil {
				t.Fatalf("expected error for upstream %q, got nil", tc.upstream)
			}
		})
	}
}

// TestSaveDNSSettingsRejectsUnboundConfInjection blocks payloads
// crafted to break out of the `forward-addr:` line and inject
// sibling directives like `forward-tls-upstream: no` or extra
// forward-addr targets. Real DoT upstreams only need alphanumerics,
// `.-:@#`.
func TestSaveDNSSettingsRejectsUnboundConfInjection(t *testing.T) {
	cases := []struct {
		name     string
		upstream string
	}{
		{"newline injection", "1.1.1.1#cloudflare-dns.com\nforward-tls-upstream: no"},
		{"CR injection", "1.1.1.1#cloudflare-dns.com\rforward-addr: 6.6.6.6"},
		{"space in spec", "1.1.1.1 evil#cloudflare-dns.com"},
		{"NUL byte", "1.1.1.1\x00#cloudflare-dns.com"},
		{"quote in SNI", "1.1.1.1#cloudflare\"-dns.com"},
		{"tab in spec", "1.1.1.1\t#cloudflare-dns.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
			svc := services.NewDNSService(cfg)
			if err := svc.SaveDNSSettings(true, tc.upstream); err == nil {
				t.Fatalf("expected error for upstream %q, got nil", tc.upstream)
			}
		})
	}
}

// TestSaveDNSSettingsAllowsAnyUpstreamWhenDoTDisabled keeps the
// validator scoped to the "enabled" path so operators can stash a
// draft upstream while DoT is off.
func TestSaveDNSSettingsAllowsAnyUpstreamWhenDoTDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	svc := services.NewDNSService(cfg)
	if err := svc.SaveDNSSettings(false, "1.1.1.1"); err != nil {
		t.Fatalf("disabled DoT should accept bare IP, got %v", err)
	}
}

func TestClearQueryLog(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewDNSService(cfg)

	err := svc.ClearQueryLog(context.TODO())
	if err != nil {
		t.Fatalf("clear: %v", err)
	}

	queries := svc.GetRecentQueries(10, 0)
	if len(queries) != 0 {
		t.Error("should be empty after clear")
	}
}
