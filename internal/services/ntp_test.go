package services_test

import (
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestNTPAddSourceRejectsChronyConfInjection blocks payloads crafted
// to break out of the `server <X> iburst` line and inject sibling
// chrony directives (e.g. `allow 0.0.0.0/0`).
func TestNTPAddSourceRejectsChronyConfInjection(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"empty", ""},
		{"newline injection", "pool.ntp.org\nallow 0.0.0.0/0"},
		{"CR injection", "pool.ntp.org\rcmdallow all"},
		{"space token break", "pool.ntp.org evil"},
		{"tab token break", "pool.ntp.org\tevil"},
		{"NUL byte", "pool.ntp.org\x00"},
		{"semicolon", "pool.ntp.org;"},
		{"quote", "pool.ntp.org\""},
		{"hyphen prefix label", "-bad.ntp.org"},
		{"hyphen suffix label", "bad-.ntp.org"},
		{"empty label", "pool..ntp.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
			svc := services.NewNTPService(cfg)
			if err := svc.AddSource(tc.host); err == nil {
				t.Fatalf("expected error for host %q, got nil", tc.host)
			}
		})
	}
}

// TestNTPAddSourceAcceptsValidHosts keeps the validator usable for
// the legitimate hostname/IP shapes operators typically configure.
func TestNTPAddSourceAcceptsValidHosts(t *testing.T) {
	for _, h := range []string{
		"pool.ntp.org",
		"0.tr.pool.ntp.org",
		"time.cloudflare.com",
		"1.1.1.1",
		"2606:4700:4700::1111",
	} {
		cfg := config.DefaultConfig()
		cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
		svc := services.NewNTPService(cfg)
		if err := svc.AddSource(h); err != nil {
			t.Fatalf("expected accept for %q, got %v", h, err)
		}
	}
}

// TestNTPSaveSettingsRejectsFallbackInjection covers the same guard
// on the fallback hostname stored via SaveSettings.
func TestNTPSaveSettingsRejectsFallbackInjection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	svc := services.NewNTPService(cfg)
	bad := "fallback.example\nallow 0.0.0.0/0"
	if err := svc.SaveSettings(bad, "", 0, false, false); err == nil {
		t.Fatalf("expected fallback injection to be rejected")
	}
}

// TestNTPSaveSettingsRejectsListenAddressInjection blocks chrony.conf
// `bindaddress` injection via a newline-laced listen address.
func TestNTPSaveSettingsRejectsListenAddressInjection(t *testing.T) {
	cases := []string{
		"127.0.0.1\nallow 0.0.0.0/0",
		"127.0.0.1 evil",
		"not-an-ip",
		"hostname.example.com", // chrony bindaddress is IP-only
		"127.0.0.1\x00",
	}
	for _, addr := range cases {
		t.Run(addr, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
			svc := services.NewNTPService(cfg)
			if err := svc.SaveSettings("", addr, 0, true, false); err == nil {
				t.Fatalf("expected listen address %q to be rejected", addr)
			}
		})
	}
}

// TestNTPSaveSettingsAcceptsValidListenAddress covers the legitimate
// IP literal shapes a router operator typically configures.
func TestNTPSaveSettingsAcceptsValidListenAddress(t *testing.T) {
	for _, addr := range []string{"", "127.0.0.1", "10.10.10.1", "::1", "fe80::1"} {
		cfg := config.DefaultConfig()
		cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
		svc := services.NewNTPService(cfg)
		if err := svc.SaveSettings("", addr, 0, true, false); err != nil {
			t.Fatalf("expected accept for %q, got %v", addr, err)
		}
	}
}

// TestNTPSaveSettingsAcceptsEmptyFallback ensures clearing the
// fallback (operator's "use default" choice) round-trips cleanly.
func TestNTPSaveSettingsAcceptsEmptyFallback(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	svc := services.NewNTPService(cfg)
	if err := svc.SaveSettings("", "", 0, false, false); err != nil {
		t.Fatalf("empty fallback should be accepted, got %v", err)
	}
}
