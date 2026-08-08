package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

const defaultTLSDataDir = "/var/lib/lankeeper"

// maxSelfSignedValidDays bounds the certificate lifetime. The upper end
// is the ten years the generator already defaults to; past that the
// value stops meaning anything, because a key that old has outlived the
// machine it was issued for.
const maxSelfSignedValidDays = 3650

// maxSANs bounds the subject alternative name list. Each one is
// rendered into the certificate and shown back on the settings page, so
// an unbounded list is a way to grow the config file from a form post.
const maxSANs = 16

var (
	ErrInvalidSAN      = errors.New("a subject alternative name must be an IP address or a hostname")
	ErrTooManySANs     = fmt.Errorf("at most %d subject alternative names are allowed", maxSANs)
	ErrInvalidValidity = fmt.Errorf("validity must be between 1 and %d days", maxSelfSignedValidDays)
	ErrNotSelfSigned   = errors.New("the TLS mode is not self-signed, so there is nothing to regenerate")
)

// TLSService owns the certificate behind the web UI.
//
// It sits in services rather than in the handler because regenerating
// touches three things that have to happen in order: write the new pair
// to disk, persist the mode, and restart the unit that is serving the
// request. The handler is not the place to own an ordering whose wrong
// half locks the operator out of their own router.
type TLSService struct {
	cfg     *config.Config
	dataDir string
}

func NewTLSService(cfg *config.Config) *TLSService {
	return &TLSService{cfg: cfg, dataDir: tlsDataDir()}
}

// NewTLSServiceInDir builds the service against an explicit data
// directory. Tests use it because they cannot write under /var/lib.
func NewTLSServiceInDir(cfg *config.Config, dataDir string) *TLSService {
	return &TLSService{cfg: cfg, dataDir: dataDir}
}

// TLSDataDir resolves the certificate root, matching how the credential
// key and the first-boot marker are overridden.
//
// Exported because Serve resolves the same paths when it loads the
// keypair. Two copies of the default would agree until one of them was
// pointed somewhere else, and then the process would serve one
// certificate while the settings page reported another.
func TLSDataDir() string { return tlsDataDir() }

func tlsDataDir() string {
	if p := os.Getenv("LANKEEPER_DATA_DIR"); p != "" {
		return p
	}
	return defaultTLSDataDir
}

// Info reports the certificate currently on disk. It reads rather than
// generates: the settings page must be able to say "there is no
// certificate" without creating one as a side effect of being looked at.
func (s *TLSService) Info() (*config.TLSCertInfo, error) {
	return config.ReadTLSCertInfo(&s.cfg.System.TLS, s.dataDir)
}

// ValidateSAN reports whether s can go in a certificate as a subject
// alternative name. An IP literal and a hostname are both accepted,
// because the operator reaches this router by both.
func ValidateSAN(s string) error {
	if net.ParseIP(s) != nil {
		return nil
	}
	if err := ValidateDomain(s); err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidSAN, s)
	}
	return nil
}

// ParseSANs splits a comma or whitespace separated list and validates
// every entry. An empty list is allowed and means "let the generator
// pick", which is what a fresh install gets.
func ParseSANs(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) > maxSANs {
		return nil, ErrTooManySANs
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if err := ValidateSAN(f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// Regenerate issues a fresh self-signed pair and restarts the service so
// the new certificate is the one presented.
//
// The certificate is written before anything else changes, and the
// config is persisted only after that write succeeded. The restart comes
// last. Getting this backwards, persisting first, would leave a config
// pointing at a certificate that was never produced, and the unit would
// fail to bind on the way back up with the operator locked out of the
// only interface that could fix it.
func (s *TLSService) Regenerate(ctx context.Context, cn string, sans []string, validDays int) (*config.TLSCertInfo, error) {
	if mode := s.cfg.System.TLS.Mode; mode != "" && mode != "self-signed" {
		return nil, fmt.Errorf("%w: mode is %q", ErrNotSelfSigned, mode)
	}
	if err := ValidateDomain(cn); err != nil {
		return nil, err
	}
	if validDays < 1 || validDays > maxSelfSignedValidDays {
		return nil, fmt.Errorf("%w: %d", ErrInvalidValidity, validDays)
	}
	for _, san := range sans {
		if err := ValidateSAN(san); err != nil {
			return nil, err
		}
	}
	if len(sans) > maxSANs {
		return nil, ErrTooManySANs
	}

	// Work on a copy so a failed generation leaves the live config
	// exactly as it was. Writing the fields first and rolling them back
	// in an error path is the same thing with a window in it.
	next := s.cfg.System.TLS
	next.Mode = "self-signed"
	next.SelfSigned = config.SelfSignedConfig{CN: cn, ValidDays: validDays, SANs: sans}

	info, err := config.RegenerateSelfSigned(&next, s.dataDir)
	if err != nil {
		return nil, fmt.Errorf("generate certificate: %w", err)
	}

	s.cfg.System.TLS = next
	if err := s.cfg.SaveToFile(); err != nil {
		return nil, fmt.Errorf("persist tls settings: %w", err)
	}
	return info, nil
}

// Restart brings the service back on the new certificate. Separate from
// Regenerate so the caller can answer the request before the process
// that is answering it goes away.
func (s *TLSService) Restart(ctx context.Context) error {
	if _, err := netutil.Run(ctx, "systemctl", "restart", "lankeeper.target"); err != nil {
		return fmt.Errorf("restart service: %w", err)
	}
	return nil
}
