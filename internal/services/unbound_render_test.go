package services

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// renderShippedUnbound renders configs/sysconf/unbound.conf.tmpl, which
// RenderConfig reads relative to the repository root.
func renderShippedUnbound(t *testing.T, cfg *config.Config) string {
	t.Helper()
	t.Chdir("../..")
	out, err := NewDNSService(cfg).RenderConfig()
	if err != nil {
		t.Fatalf("render unbound.conf: %v", err)
	}
	return out
}

// TestDoTForwardingVerifiesTheUpstream is the regression test. With
// forward-tls-upstream on and no trust anchor, Unbound does not verify
// the upstream certificate or the #name.
func TestDoTForwardingVerifiesTheUpstream(t *testing.T) {
	cfg := &config.Config{}
	cfg.DNS.EnableDoT = true
	cfg.DNS.DoTUpstream = "1.1.1.1@853#cloudflare-dns.com"
	out := renderShippedUnbound(t, cfg)
	if !strings.Contains(out, `tls-cert-bundle: "/etc/ssl/certs/ca-certificates.crt"`) {
		t.Errorf("DoT forwarding has no CA bundle:\n%s", out)
	}
}
