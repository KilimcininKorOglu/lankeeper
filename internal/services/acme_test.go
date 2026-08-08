package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func newACMETestService(t *testing.T) (*services.ACMEService, *config.Config, string) {
	t.Helper()
	tlsSvc, cfg, dir := newTLSTestService(t)
	t.Setenv("LANKEEPER_ACME_KEY", filepath.Join(dir, "acme-account.key"))
	// The directory URL is a stub: every test here stops before the
	// first request to it. The ones that would reach a CA cannot run
	// without one, and standing up a full ACME server to assert on the
	// parts this code owns would test x/crypto/acme instead.
	return services.NewACMEServiceInDir(cfg, tlsSvc, dir, "https://acme.invalid/directory", "https://cf.invalid"), cfg, dir
}

// The account key is the installation's identity at the CA. A new key is
// a new account with none of the authorizations the old one earned, so
// every renewal would revalidate from scratch and eat rate limit.
func TestACMEAccountKeyIsCreatedOnceAndReused(t *testing.T) {
	svc, cfg, dir := newACMETestService(t)
	cfg.System.TLS.ACME.Domain = "hermes.example"
	cfg.System.TLS.ACME.Email = "ops@example.com"

	keyPath := filepath.Join(dir, "acme-account.key")

	// Issue reaches the CA and fails, which is expected here. What
	// matters is that the key was minted on the way.
	_, _ = svc.Issue(context.Background())

	first, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("account key was not created: %v", err)
	}
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat account key: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("account key mode = %04o, want 0600", perm)
	}

	_, _ = svc.Issue(context.Background())
	second, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read account key: %v", err)
	}
	if string(first) != string(second) {
		t.Error("the account key was regenerated; every renewal would start a new CA account")
	}
}

// Input the CA would reject has to be refused here, before an account is
// registered and before any rate limit is spent.
func TestIssueRefusesIncompleteConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		domain string
		email  string
		want   error
	}{
		{"no domain", "", "ops@example.com", services.ErrACMEDomainRequired},
		{"domain with a newline", "a\nb", "ops@example.com", services.ErrInvalidDomain},
		{"no email", "hermes.example", "", services.ErrACMEEmailRequired},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, cfg, dir := newACMETestService(t)
			cfg.System.TLS.ACME.Domain = tc.domain
			cfg.System.TLS.ACME.Email = tc.email

			_, err := svc.Issue(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "acme-account.key")); !os.IsNotExist(statErr) {
				t.Error("an account key was minted for a configuration that was refused")
			}
		})
	}
}

// An unrecognised provider must stop the flow rather than fall through
// to a default that quietly asks the operator to publish records by hand
// when they configured an API.
func TestIssueRefusesAnUnknownProvider(t *testing.T) {
	svc, cfg, _ := newACMETestService(t)
	cfg.System.TLS.ACME.Domain = "hermes.example"
	cfg.System.TLS.ACME.Email = "ops@example.com"
	cfg.System.TLS.ACME.DNSChallenge.Provider = "route53"

	_, err := svc.Issue(context.Background())
	if !errors.Is(err, services.ErrACMEUnknownProvider) {
		t.Fatalf("error = %v, want ErrACMEUnknownProvider", err)
	}
}

// Production allows five certificates per registered domain per week, so
// an unset or unrecognised choice has to land on staging. The other way
// round, a config mistake costs the operator days.
func TestACMEDirectoryDefaultsToStaging(t *testing.T) {
	cases := map[string]string{
		"production":   services.LetsEncryptProductionURL,
		"staging":      services.LetsEncryptStagingURL,
		"":             services.LetsEncryptStagingURL,
		"PRODUCTION":   services.LetsEncryptStagingURL,
		"https://evil": services.LetsEncryptStagingURL,
	}
	for choice, want := range cases {
		if got := services.ACMEDirectoryForChoice(choice); got != want {
			t.Errorf("ACMEDirectoryForChoice(%q) = %q, want %q", choice, got, want)
		}
	}
}

// The renewal loop must be inert unless ACME is the active mode, or a
// router on a self-signed certificate would start talking to a CA.
func TestRenewalIsInertOutsideACMEMode(t *testing.T) {
	svc, cfg, dir := newACMETestService(t)
	cfg.System.TLS.Mode = "self-signed"
	cfg.System.TLS.ACME.Enabled = false

	svc.RenewIfDue(context.Background())

	if _, err := os.Stat(filepath.Join(dir, "acme-account.key")); !os.IsNotExist(err) {
		t.Error("the renewal loop contacted the CA while the mode was self-signed")
	}
}

// A certificate with plenty of life left must not be reissued. Renewing
// on every tick would spend rate limit for nothing.
func TestRenewalSkipsACertificateThatIsNotDue(t *testing.T) {
	tlsSvc, cfg, dir := newTLSTestService(t)
	t.Setenv("LANKEEPER_ACME_KEY", filepath.Join(dir, "acme-account.key"))
	svc := services.NewACMEServiceInDir(cfg, tlsSvc, dir, "https://acme.invalid/directory", "https://cf.invalid")

	if _, err := tlsSvc.Regenerate(context.Background(), "hermes.example", nil, 90); err != nil {
		t.Fatalf("seed certificate: %v", err)
	}
	cfg.System.TLS.Mode = "acme"
	cfg.System.TLS.ACME.Enabled = true
	cfg.System.TLS.ACME.Domain = "hermes.example"
	cfg.System.TLS.ACME.Email = "ops@example.com"

	svc.RenewIfDue(context.Background())

	if _, err := os.Stat(filepath.Join(dir, "acme-account.key")); !os.IsNotExist(err) {
		t.Error("a certificate with 90 days left triggered a renewal")
	}
}

// Inside the window it must act. The window is thirty days wide against
// a twelve-hour tick, so a missed trigger has to be visible as a
// deliberate decision rather than an off-by-one nobody notices until a
// certificate expires.
func TestRenewalActsInsideTheWindow(t *testing.T) {
	tlsSvc, cfg, dir := newTLSTestService(t)
	t.Setenv("LANKEEPER_ACME_KEY", filepath.Join(dir, "acme-account.key"))
	svc := services.NewACMEServiceInDir(cfg, tlsSvc, dir, "https://acme.invalid/directory", "https://cf.invalid")

	if _, err := tlsSvc.Regenerate(context.Background(), "hermes.example", nil, 10); err != nil {
		t.Fatalf("seed certificate: %v", err)
	}
	cfg.System.TLS.Mode = "acme"
	cfg.System.TLS.ACME.Enabled = true
	cfg.System.TLS.ACME.Domain = "hermes.example"
	cfg.System.TLS.ACME.Email = "ops@example.com"

	svc.RenewIfDue(context.Background())

	// Issuance fails: the directory is unreachable. The account key is
	// minted before the first request, so its presence is the evidence
	// that renewal was attempted.
	if _, err := os.Stat(filepath.Join(dir, "acme-account.key")); err != nil {
		t.Errorf("a certificate with 10 days left did not trigger a renewal: %v", err)
	}
	if cfg.System.TLS.Mode != "acme" {
		t.Error("a failed renewal changed the mode")
	}
}
