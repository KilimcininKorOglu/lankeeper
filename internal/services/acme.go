package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// LetsEncryptStagingURL is the rate-limit-free directory. Production
// allows five certificates per registered domain per week, and a config
// mistake that burns them leaves the operator waiting days, so anything
// resembling a first attempt belongs here.
const (
	LetsEncryptProductionURL = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStagingURL    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// renewBefore is how long ahead of expiry the renewal loop acts. Let's
// Encrypt issues for 90 days and recommends renewing at 30, which leaves
// a fortnight of daily retries before anything breaks.
const renewBefore = 30 * 24 * time.Hour

// renewCheckInterval is how often the loop looks. The certificate it
// watches has a three-month life, so checking more often than daily
// buys nothing and only adds wakeups.
const renewCheckInterval = 12 * time.Hour

const defaultACMEKeyPath = "/var/lib/lankeeper/credentials/acme-account.key"

var (
	ErrACMEDomainRequired  = errors.New("a domain is required to request a certificate")
	ErrACMEEmailRequired   = errors.New("an account email is required to register with the CA")
	ErrACMEUnknownProvider = errors.New("unknown DNS-01 provider")
	ErrACMENoDNSChallenge  = errors.New("the CA offered no dns-01 challenge for this domain")
	// ErrManualRecordPending is not a failure. It reports that the
	// operator has to publish a TXT record before issuance can carry
	// on, and it carries the record to publish.
	ErrManualRecordPending = errors.New("publish the DNS record, then run this again")
)

// ManualRecord is the TXT record the operator has to publish by hand.
type ManualRecord struct {
	Name  string
	Value string
}

// ManualChallengeError carries the record alongside the sentinel, so the
// handler can render it without a second lookup and errors.Is still
// identifies the condition.
type ManualChallengeError struct {
	Record ManualRecord
}

func (e *ManualChallengeError) Error() string {
	return fmt.Sprintf("%s: %s TXT %s", ErrManualRecordPending, e.Record.Name, e.Record.Value)
}

func (e *ManualChallengeError) Unwrap() error { return ErrManualRecordPending }

// dnsProvider publishes and withdraws the DNS-01 TXT record.
//
// An interface rather than a switch inside the issuance flow because the
// flow is the part that cannot be tested against a real CA from here.
// Keeping the provider separate means the Cloudflare request shaping and
// the ordering around it are both reachable by a test.
type dnsProvider interface {
	// Present publishes fqdn as a TXT record holding value.
	Present(ctx context.Context, fqdn, value string) error
	// CleanUp withdraws it. Called on every exit path, including the
	// failures: a stale _acme-challenge record is a standing statement
	// about this domain that nobody meant to leave behind.
	CleanUp(ctx context.Context, fqdn, value string) error
}

// ACMEService obtains and renews a certificate over DNS-01.
//
// DNS-01 rather than HTTP-01 because this router's web UI is LAN-only by
// design. HTTP-01 needs the CA to reach port 80 from the internet, which
// would mean opening the one thing the firewall exists to keep shut.
type ACMEService struct {
	cfg     *config.Config
	dataDir string
	tls     *TLSService
	// directoryURL is settable so tests can point at a local server and
	// so the operator can choose staging.
	directoryURL string
	// cloudflareAPI is the Cloudflare v4 base. Settable for the same
	// reason.
	cloudflareAPI string
}

func NewACMEService(cfg *config.Config, tlsSvc *TLSService) *ACMEService {
	return &ACMEService{
		cfg:           cfg,
		dataDir:       tlsDataDir(),
		tls:           tlsSvc,
		directoryURL:  acmeDirectoryURL(cfg),
		cloudflareAPI: cloudflareAPIBase,
	}
}

// NewACMEServiceInDir builds the service against explicit endpoints.
// Tests use it to stand up a local directory and a local Cloudflare.
func NewACMEServiceInDir(cfg *config.Config, tlsSvc *TLSService, dataDir, directoryURL, cloudflareAPI string) *ACMEService {
	return &ACMEService{
		cfg:           cfg,
		dataDir:       dataDir,
		tls:           tlsSvc,
		directoryURL:  directoryURL,
		cloudflareAPI: cloudflareAPI,
	}
}

// acmeDirectoryURL picks the CA endpoint. Staging is the default when
// nothing is configured: the cost of a wrong guess is a certificate no
// browser trusts, against a week-long lockout the other way round.
func acmeDirectoryURL(cfg *config.Config) string {
	if cfg.System.TLS.ACME.DirectoryURL != "" {
		return cfg.System.TLS.ACME.DirectoryURL
	}
	return LetsEncryptStagingURL
}

// ACMEDirectoryForChoice maps the form's endpoint selector onto a
// directory URL.
//
// A two-value allowlist rather than a free-text field. The URL is
// dialled by this process with the account key attached, and letting the
// operator type one would make the CA an arbitrary destination.
func ACMEDirectoryForChoice(choice string) string {
	if choice == "production" {
		return LetsEncryptProductionURL
	}
	return LetsEncryptStagingURL
}

// acmeAccountKeyPath resolves where the account key lives. The override
// exists for tests, matching the config key and the first-boot marker.
func acmeAccountKeyPath() string {
	if p := os.Getenv("LANKEEPER_ACME_KEY"); p != "" {
		return p
	}
	return defaultACMEKeyPath
}

// loadOrCreateAccountKey returns the key identifying this installation
// to the CA.
//
// It has to persist. A new key is a new account, and a new account has
// none of the authorizations the old one accumulated, so losing it turns
// every renewal into a fresh validation and burns rate limit.
func loadOrCreateAccountKey() (*ecdsa.PrivateKey, error) {
	path := acmeAccountKeyPath()

	// path comes from a constant or an environment variable set by the
	// operator, never from a request.
	// #nosec G304
	if raw, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("decode account key: no valid PEM block")
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse account key: %w", err)
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read account key: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal account key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create credentials dir: %w", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, fmt.Errorf("write account key: %w", err)
	}
	return key, nil
}

// provider builds the DNS-01 publisher named in the config.
func (s *ACMEService) provider() (dnsProvider, error) {
	switch s.cfg.System.TLS.ACME.DNSChallenge.Provider {
	case "cloudflare":
		return &cloudflareProvider{
			apiBase: s.cloudflareAPI,
			token:   s.cfg.System.TLS.ACME.DNSChallenge.APIToken,
		}, nil
	case "manual", "":
		return &manualProvider{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrACMEUnknownProvider, s.cfg.System.TLS.ACME.DNSChallenge.Provider)
	}
}

// Issue runs the full DNS-01 flow and installs the result.
//
// The certificate is installed and the mode is recorded only after the
// CA has handed one over. Recording "acme" first would leave a config
// the server cannot bind against, and the only way to correct it is the
// interface that would then be down.
func (s *ACMEService) Issue(ctx context.Context) (*config.TLSCertInfo, error) {
	acmeCfg := s.cfg.System.TLS.ACME
	domain := strings.TrimSpace(acmeCfg.Domain)
	if domain == "" {
		return nil, ErrACMEDomainRequired
	}
	if err := ValidateDomain(domain); err != nil {
		return nil, err
	}
	if strings.TrimSpace(acmeCfg.Email) == "" {
		return nil, ErrACMEEmailRequired
	}

	prov, err := s.provider()
	if err != nil {
		return nil, err
	}

	accountKey, err := loadOrCreateAccountKey()
	if err != nil {
		return nil, err
	}

	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: s.directoryURL,
		// The guarded client, not a bare one. This process can reach
		// every segment behind it, and a directory URL is operator
		// input, so the same rule that covers blocklist downloads
		// covers the CA.
		HTTPClient: outboundFetchClient,
		UserAgent:  "lankeeper",
	}

	// Registration is idempotent: an existing account comes back as
	// ErrAccountAlreadyExists, which is a success for our purposes.
	acct := &acme.Account{Contact: []string{"mailto:" + acmeCfg.Email}}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("register acme account: %w", err)
	}

	order, err := client.AuthorizeOrder(ctx, []acme.AuthzID{{Type: "dns", Value: domain}})
	if err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}

	for _, authzURL := range order.AuthzURLs {
		if err := s.satisfy(ctx, client, prov, authzURL); err != nil {
			return nil, err
		}
	}

	if order, err = client.WaitOrder(ctx, order.URI); err != nil {
		return nil, fmt.Errorf("wait for order: %w", err)
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate certificate key: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domain},
		DNSNames: []string{domain},
	}, certKey)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}

	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("finalize order: %w", err)
	}

	var certPEM []byte
	for _, b := range der {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return nil, fmt.Errorf("marshal certificate key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	next := s.cfg.System.TLS
	next.Mode = "acme"
	next.ACME.Enabled = true

	info, err := config.InstallTLSPair(&next, s.dataDir, certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("install certificate: %w", err)
	}

	s.cfg.System.TLS = next
	if err := s.cfg.SaveToFile(); err != nil {
		return nil, fmt.Errorf("persist tls settings: %w", err)
	}
	return info, nil
}

// satisfy completes one authorization over dns-01.
func (s *ACMEService) satisfy(ctx context.Context, client *acme.Client, prov dnsProvider, authzURL string) error {
	authz, err := client.GetAuthorization(ctx, authzURL)
	if err != nil {
		return fmt.Errorf("get authorization: %w", err)
	}
	// An authorization the account already satisfied comes back valid.
	// Re-running the challenge for it would be a wasted round trip and,
	// on the manual provider, would ask the operator to publish a
	// record that is not needed.
	if authz.Status == acme.StatusValid {
		return nil
	}

	var chal *acme.Challenge
	for _, c := range authz.Challenges {
		if c.Type == "dns-01" {
			chal = c
			break
		}
	}
	if chal == nil {
		return fmt.Errorf("%w: %s", ErrACMENoDNSChallenge, authz.Identifier.Value)
	}

	value, err := client.DNS01ChallengeRecord(chal.Token)
	if err != nil {
		return fmt.Errorf("compute challenge record: %w", err)
	}
	fqdn := "_acme-challenge." + authz.Identifier.Value

	if err := prov.Present(ctx, fqdn, value); err != nil {
		return err
	}
	// Withdrawn on every path out, including the failures. A leftover
	// _acme-challenge record is a standing assertion about this domain
	// that nobody meant to leave published.
	defer func() {
		if err := prov.CleanUp(context.WithoutCancel(ctx), fqdn, value); err != nil {
			log.Printf("acme: remove challenge record: %v", err)
		}
	}()

	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("accept challenge: %w", err)
	}
	if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
		return fmt.Errorf("wait for validation: %w", err)
	}
	return nil
}

// StartRenewal runs the renewal loop until ctx is cancelled.
//
// Started from Serve rather than from the constructor, so it shares the
// shutdown context and the background wait group. A goroutine launched
// at construction outlives every attempt to stop the server.
func (s *ACMEService) StartRenewal(ctx context.Context) {
	ticker := time.NewTicker(renewCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.RenewIfDue(ctx)
		}
	}
}

// RenewIfDue reissues when the certificate is inside the renewal window,
// and does nothing otherwise. Exported because it is the loop's whole
// body: a test that drives it directly checks the decision without
// waiting twelve hours for a tick.
func (s *ACMEService) RenewIfDue(ctx context.Context) {
	if s.cfg.System.TLS.Mode != "acme" || !s.cfg.System.TLS.ACME.Enabled {
		return
	}
	info, err := config.ReadTLSCertInfo(&s.cfg.System.TLS, s.dataDir)
	if err != nil {
		log.Printf("acme: read current certificate: %v", err)
		return
	}
	if time.Until(info.NotAfter) > renewBefore {
		return
	}

	log.Printf("acme: certificate expires %s, renewing", info.NotAfter)
	if _, err := s.Issue(ctx); err != nil {
		// Logged and dropped on purpose. The loop retries twice a day
		// and the renewal window is thirty days wide, so a transient
		// failure has sixty more chances before anything is served an
		// expired certificate.
		log.Printf("acme: renewal failed: %v", err)
		return
	}
	log.Println("acme: certificate renewed")
}
