package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func newTLSTestService(t *testing.T) (*services.TLSService, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.System.TLS.Mode = "self-signed"
	// The service persists on every accepted change, so the config
	// needs somewhere to write or a passing regeneration would fail on
	// the save rather than on anything the test is about.
	cfg.SetFilePath(filepath.Join(dir, "router.yaml"))
	t.Setenv("LANKEEPER_CONFIG_KEY", filepath.Join(dir, "config.key"))
	return services.NewTLSServiceInDir(cfg, dir), cfg, dir
}

// The certificate the operator asked for is the one that gets served.
// EnsureTLSCert keeps an unexpired pair, which is right at boot and
// wrong here: a changed CN would have been persisted and then ignored
// until the old certificate happened to lapse.
func TestRegenerateReplacesAnUnexpiredCertificate(t *testing.T) {
	svc, _, dir := newTLSTestService(t)

	first, err := svc.Regenerate(context.Background(), "hermes.lan", nil, 3650)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Regenerate(context.Background(), "athena.lan", nil, 3650)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if second.Issuer != "athena.lan" {
		t.Errorf("issuer = %q, want athena.lan: the new name was not applied", second.Issuer)
	}
	if first.NotAfter.Equal(second.NotAfter) && first.Issuer == second.Issuer {
		t.Error("the certificate was not replaced")
	}
	if _, err := os.Stat(filepath.Join(dir, "tls", "server.key")); err != nil {
		t.Errorf("private key missing after regeneration: %v", err)
	}
}

// The SANs are what a browser actually matches against, so a request
// naming an IP has to produce a certificate carrying that IP rather
// than a DNS entry that looks like one.
func TestRegenerateCarriesTheRequestedSANs(t *testing.T) {
	svc, cfg, _ := newTLSTestService(t)

	info, err := svc.Regenerate(context.Background(), "hermes.lan", []string{"hermes.lan", "10.10.10.1"}, 30)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}

	want := map[string]bool{"hermes.lan": false, "10.10.10.1": false}
	for _, san := range info.SANs {
		if _, ok := want[san]; ok {
			want[san] = true
		}
	}
	for san, seen := range want {
		if !seen {
			t.Errorf("certificate does not carry SAN %q; it has %v", san, info.SANs)
		}
	}
	if got := len(cfg.System.TLS.SelfSigned.SANs); got != 2 {
		t.Errorf("persisted SANs = %d, want 2", got)
	}
}

// A validity the operator can read back: asking for 30 days must not
// silently become the ten-year default.
func TestRegenerateHonoursTheRequestedValidity(t *testing.T) {
	svc, _, _ := newTLSTestService(t)

	info, err := svc.Regenerate(context.Background(), "hermes.lan", nil, 30)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}

	days := info.NotAfter.Sub(info.NotBefore).Hours() / 24
	if days < 29 || days > 31 {
		t.Errorf("validity = %.0f days, want 30", days)
	}
}

// Rejected input must leave the live config untouched. Writing the
// fields first and undoing them on error is the same thing with a
// window in it, and the window is one where the config on disk names a
// certificate that was never produced.
func TestRegenerateLeavesConfigIntactWhenInputIsRejected(t *testing.T) {
	svc, cfg, dir := newTLSTestService(t)
	cfg.System.TLS.SelfSigned = config.SelfSignedConfig{CN: "keep.lan", ValidDays: 100}

	cases := []struct {
		name      string
		cn        string
		sans      []string
		validDays int
	}{
		{"empty common name", "", nil, 30},
		{"common name with a newline", "a\nb", nil, 30},
		{"validity of zero", "hermes.lan", nil, 0},
		{"validity past the ceiling", "hermes.lan", nil, 3651},
		{"san that is neither host nor ip", "hermes.lan", []string{"not a host"}, 30},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Regenerate(context.Background(), tc.cn, tc.sans, tc.validDays); err == nil {
				t.Fatal("accepted invalid input")
			}
			if cfg.System.TLS.SelfSigned.CN != "keep.lan" {
				t.Errorf("CN was overwritten to %q despite the failure", cfg.System.TLS.SelfSigned.CN)
			}
			if cfg.System.TLS.SelfSigned.ValidDays != 100 {
				t.Errorf("ValidDays was overwritten to %d despite the failure", cfg.System.TLS.SelfSigned.ValidDays)
			}
			if _, err := os.Stat(filepath.Join(dir, "tls", "server.crt")); !os.IsNotExist(err) {
				t.Error("a certificate was written for input that was refused")
			}
		})
	}
}

// Regenerating under mkcert or ACME would overwrite a certificate this
// service did not issue and cannot reissue, and the mode would still
// say the certificate came from elsewhere.
func TestRegenerateRefusesWhenTheModeIsNotSelfSigned(t *testing.T) {
	svc, cfg, _ := newTLSTestService(t)
	cfg.System.TLS.Mode = "mkcert"

	_, err := svc.Regenerate(context.Background(), "hermes.lan", nil, 30)
	if !errors.Is(err, services.ErrNotSelfSigned) {
		t.Fatalf("error = %v, want ErrNotSelfSigned", err)
	}
}

// Info reports rather than creates. Rendering the settings page must
// not be what brings a key pair into existence, or "there is no
// certificate" becomes an unreachable state.
func TestInfoDoesNotGenerateACertificate(t *testing.T) {
	svc, _, dir := newTLSTestService(t)

	if _, err := svc.Info(); err == nil {
		t.Fatal("Info reported a certificate before one was issued")
	}
	if _, err := os.Stat(filepath.Join(dir, "tls", "server.crt")); !os.IsNotExist(err) {
		t.Error("Info created a certificate as a side effect of being read")
	}
}

func TestInfoReportsTheIssuedCertificate(t *testing.T) {
	svc, _, _ := newTLSTestService(t)

	issued, err := svc.Regenerate(context.Background(), "hermes.lan", nil, 30)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	read, err := svc.Info()
	if err != nil {
		t.Fatalf("info: %v", err)
	}

	if !read.NotAfter.Equal(issued.NotAfter) || read.Issuer != issued.Issuer {
		t.Errorf("Info reported %s/%v, want %s/%v", read.Issuer, read.NotAfter, issued.Issuer, issued.NotAfter)
	}
	if read.NotAfter.Before(time.Now()) {
		t.Error("the freshly issued certificate is already expired")
	}
}

func TestParseSANsSplitsAndValidates(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
		ok   bool
	}{
		{"comma separated", "a.lan, b.lan", []string{"a.lan", "b.lan"}, true},
		{"whitespace separated", "a.lan b.lan", []string{"a.lan", "b.lan"}, true},
		{"ip literal", "10.10.10.1", []string{"10.10.10.1"}, true},
		{"empty means let the generator decide", "", nil, true},
		{"embedded space in a name", "a lan,", []string{"a", "lan"}, true},
		{"newline injection", "a.lan\nb", nil, true},
		{"slash is not a hostname", "a/b", nil, false},
		{"over the ceiling", "a.lan,b.lan,c.lan,d.lan,e.lan,f.lan,g.lan,h.lan,i.lan,j.lan,k.lan,l.lan,m.lan,n.lan,o.lan,p.lan,q.lan", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := services.ParseSANs(tc.raw)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if !tc.ok {
				return
			}
			if tc.want != nil && len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
