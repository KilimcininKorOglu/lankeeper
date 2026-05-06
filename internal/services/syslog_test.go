package services_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// TestSyslogSaveServerRejectsPathTraversalCertFiles verifies that
// authenticated operators cannot coerce rsyslog (root) into reading
// arbitrary files via TLS cert/key/CA fields. Path-traversal hits
// /etc/shadow / WireGuard private keys / router.yaml would otherwise
// surface in PEM-parse errors on the /syslog page. (BUG-067)
func TestSyslogSaveServerRejectsPathTraversalCertFiles(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.SyslogServerConfig
	}{
		{
			"shadow as cert",
			config.SyslogServerConfig{TLSCertFile: "/etc/shadow"},
		},
		{
			"router.yaml as key",
			config.SyslogServerConfig{TLSKeyFile: "/etc/lankeeper/../lankeeper/router.yaml"},
		},
		{
			"wg private key as CA",
			config.SyslogServerConfig{TLSCAFile: "/etc/wireguard/wg0.key"},
		},
		{
			"relative path",
			config.SyslogServerConfig{TLSCertFile: "etc/ssl/cert.pem"},
		},
		{
			"traversal segments",
			config.SyslogServerConfig{TLSCertFile: "/etc/ssl/../shadow"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
			svc := services.NewSyslogService(cfg)
			if err := svc.SaveServerConfig(tc.cfg); err == nil {
				t.Fatalf("expected error for %+v, got nil", tc.cfg)
			}
		})
	}
}

// TestSyslogSaveServerAcceptsAllowlistedPaths covers the legitimate
// operator paths so the validator stays usable.
func TestSyslogSaveServerAcceptsAllowlistedPaths(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	svc := services.NewSyslogService(cfg)
	good := config.SyslogServerConfig{
		TLSCertFile: "/etc/ssl/certs/syslog.pem",
		TLSKeyFile:  "/etc/ssl/private/syslog.key",
		TLSCAFile:   "/etc/lankeeper/ca.pem",
	}
	if err := svc.SaveServerConfig(good); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

// TestSyslogSaveServerEmptyPathsAccepted ensures clearing a TLS path
// (operator disabling TLS material) round-trips cleanly. The rsyslog
// template emits a commented-out directive so no file is opened.
func TestSyslogSaveServerEmptyPathsAccepted(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	svc := services.NewSyslogService(cfg)
	if err := svc.SaveServerConfig(config.SyslogServerConfig{}); err != nil {
		t.Fatalf("empty TLS paths should be accepted, got %v", err)
	}
}

// TestSyslogSaveClientRejectsPathTraversalCAFile mirrors the server
// test for the client-side CA field, which feeds the same rsyslog
// directive on a forwarding client.
func TestSyslogSaveClientRejectsPathTraversalCAFile(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	svc := services.NewSyslogService(cfg)
	bad := config.SyslogClientConfig{TLSCAFile: "/etc/shadow"}
	err := svc.SaveClientConfig(bad)
	if err == nil {
		t.Fatal("expected error for /etc/shadow CA, got nil")
	}
	if !strings.Contains(err.Error(), "tls_ca_file") {
		t.Errorf("error should name the rejected field, got %q", err.Error())
	}
}
