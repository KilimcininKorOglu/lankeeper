package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

func TestGenerateSelfSignedCert(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.TLSConfig{
		Mode: "self-signed",
		SelfSigned: config.SelfSignedConfig{
			CN:        "test-router.lan",
			ValidDays: 365,
			SANs:      []string{"test-router.lan", "10.10.10.1", "127.0.0.1"},
		},
	}

	info, err := config.EnsureTLSCert(cfg, dir)
	if err != nil {
		t.Fatalf("ensure tls cert: %v", err)
	}

	if info.Issuer != "test-router.lan" {
		t.Errorf("issuer = %q, want test-router.lan", info.Issuer)
	}

	certPath := filepath.Join(dir, "tls", "server.crt")
	keyPath := filepath.Join(dir, "tls", "server.key")

	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		t.Error("cert file should exist")
	}
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("key file should exist")
	}

	if len(info.SANs) == 0 {
		t.Error("SANs should not be empty")
	}
	if !slices.Contains(info.SANs, "test-router.lan") {
		t.Error("SANs should contain DNS name")
	}
	if !slices.Contains(info.SANs, "10.10.10.1") {
		t.Error("SANs should contain IP address")
	}
}

func TestEnsureTLSCertIdempotent(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.TLSConfig{
		Mode: "self-signed",
		SelfSigned: config.SelfSignedConfig{
			CN:        "test.lan",
			ValidDays: 365,
			SANs:      []string{"test.lan"},
		},
	}

	info1, err := config.EnsureTLSCert(cfg, dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	info2, err := config.EnsureTLSCert(cfg, dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if !info1.NotAfter.Equal(info2.NotAfter) {
		t.Errorf("cert should not be regenerated when still valid (notAfter1=%v, notAfter2=%v)", info1.NotAfter, info2.NotAfter)
	}
}
