package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
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
	ErrNoSANs          = errors.New("mkcert needs at least one name or address to issue for")
	ErrNoMkcertCA      = errors.New("no mkcert CA certificate on disk")
	ErrUnknownTLSMode  = errors.New("unknown TLS mode")
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
	// The guard is on this entry point rather than on the issuance
	// below, because switching modes deliberately is a different act
	// from pressing regenerate on a page that is showing a certificate
	// somebody else issued.
	if mode := s.cfg.System.TLS.Mode; mode != "" && mode != "self-signed" {
		return nil, fmt.Errorf("%w: mode is %q", ErrNotSelfSigned, mode)
	}
	return s.issueSelfSigned(ctx, cn, sans, validDays)
}

// SwitchMode moves TLS to mode, issuing whatever that mode needs before
// the mode itself is recorded.
//
// Every branch issues first and persists second. A config naming a mode
// with no certificate under it is a service that cannot bind, and the
// only interface that could correct it is the one that just stopped
// answering.
func (s *TLSService) SwitchMode(ctx context.Context, mode, cn string, sans []string, validDays int) (*config.TLSCertInfo, error) {
	switch mode {
	case "self-signed", "":
		return s.issueSelfSigned(ctx, cn, sans, validDays)
	case "mkcert":
		return s.EnableMkcert(ctx, sans)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownTLSMode, mode)
	}
}

func (s *TLSService) issueSelfSigned(_ context.Context, cn string, sans []string, validDays int) (*config.TLSCertInfo, error) {
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

// EnableMkcert issues a certificate from the local mkcert CA and
// switches the mode over.
//
// mkcert runs through the agent, so it runs as root and everything it
// writes is owned by root. The web process runs as the service account
// and has to be able to read the private key at startup, so the pair is
// staged under the CA root and copied into place here rather than
// written to the serving paths directly. Loosening the key's mode
// instead would hand it to every local account.
//
// -install is deliberately not run. It exists to trust the CA on the
// machine mkcert runs on, and this machine is the server, not the
// client. The CA is created on first issuance either way, and the
// operator installs it on their own devices from the download below.
func (s *TLSService) EnableMkcert(ctx context.Context, sans []string) (*config.TLSCertInfo, error) {
	if len(sans) == 0 {
		return nil, ErrNoSANs
	}
	if len(sans) > maxSANs {
		return nil, ErrTooManySANs
	}
	for _, san := range sans {
		if err := ValidateSAN(san); err != nil {
			return nil, err
		}
	}

	caRoot := filepath.Join(s.dataDir, "mkcert")
	if err := netutil.MkdirAll(caRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create mkcert root: %w", err)
	}
	stageCert := filepath.Join(caRoot, "staged.crt")
	stageKey := filepath.Join(caRoot, "staged.key")

	args := append([]string{"-cert-file", stageCert, "-key-file", stageKey}, sans...)
	if _, err := netutil.Run(ctx, "mkcert", args...); err != nil {
		return nil, fmt.Errorf("issue mkcert certificate: %w", err)
	}
	// The staged pair is removed whatever happens next. Leaving a
	// readable private key behind on a failure is the worst outcome of
	// this function, and it is the one nothing else would notice.
	defer func() {
		if _, err := netutil.Run(context.WithoutCancel(ctx), "rm", "-f", stageCert, stageKey); err != nil {
			log.Printf("tls: remove staged mkcert pair: %v", err)
		}
	}()

	certPEM, err := netutil.ReadFile(stageCert)
	if err != nil {
		return nil, fmt.Errorf("read issued certificate: %w", err)
	}
	keyPEM, err := netutil.ReadFile(stageKey)
	if err != nil {
		return nil, fmt.Errorf("read issued key: %w", err)
	}

	next := s.cfg.System.TLS
	next.Mode = "mkcert"
	next.Mkcert.SANs = sans

	// Installed before the mode is persisted, and the install itself
	// parses the certificate before either file lands. A config saying
	// "mkcert" with no certificate under it is a service that cannot
	// bind, reachable only from the interface that just went away.
	info, err := config.InstallTLSPair(&next, s.dataDir, certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("install mkcert certificate: %w", err)
	}

	s.cfg.System.TLS = next
	if err := s.cfg.SaveToFile(); err != nil {
		return nil, fmt.Errorf("persist tls settings: %w", err)
	}
	return info, nil
}

// MkcertCA returns the local CA certificate for the operator to install
// on their LAN devices. Without it every device shows a warning, which
// is the whole reason to run mkcert instead of a self-signed pair.
func (s *TLSService) MkcertCA() ([]byte, error) {
	pem, err := netutil.ReadFile(filepath.Join(s.dataDir, "mkcert", "rootCA.pem"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoMkcertCA, err)
	}
	return pem, nil
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
