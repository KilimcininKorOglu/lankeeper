package services

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// SystemService owns the privileged operations behind the settings
// page: the root account password, the system hostname, the timezone
// and reboot.
//
// These used to be issued straight from the HTTP handler, the only one
// in the tree that called netutil directly. That put command
// construction and hash handling somewhere no test could reach without
// building a request and faking the agent, and in practice none of it
// was covered at all, including the path that rewrites the root
// account's password.
type SystemService struct{}

func NewSystemService() *SystemService {
	return &SystemService{}
}

// hostnamePattern is the RFC 1123 label form: letters, digits and
// interior hyphens. Anything else ends up in unbound and dnsmasq
// configuration and as an argument to hostnamectl, so it is rejected
// here rather than passed along.
var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// timezonePattern matches a tz database name such as Europe/Istanbul or
// UTC. Deliberately narrow: this value reaches timedatectl and is
// persisted into the config.
var timezonePattern = regexp.MustCompile(`^[A-Za-z0-9+_-]+(/[A-Za-z0-9+_-]+){0,2}$`)

// domainPattern is a sequence of RFC 1123 labels separated by dots.
//
// The hostname beside it on the settings form was validated and this
// was not, even though it lands somewhere sharper: dnsmasq.conf.tmpl
// renders `domain={{ .Domain }}` and the RA drop-in renders it into a
// dhcp-option line. Every configs/sysconf template is text/template,
// which performs no escaping, so a newline here ended the directive and
// appended another one to a file the root agent writes. dnsmasq
// directives include dhcp-script, which runs a program.
var domainPattern = regexp.MustCompile(
	`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// maxDomainLength is the RFC 1035 limit on a fully qualified name. The
// pattern bounds each label; this bounds the whole.
const maxDomainLength = 253

// minRootPasswordLength matches the web password rule so the two
// account paths do not disagree on what is acceptable.
const minRootPasswordLength = 8

var (
	ErrInvalidHostname   = fmt.Errorf("hostname must be 1-63 characters of letters, digits and interior hyphens")
	ErrInvalidDomain     = fmt.Errorf("domain must be dot-separated labels of letters, digits and interior hyphens")
	ErrInvalidTimezone   = fmt.Errorf("timezone must be a tz database name such as Europe/Istanbul")
	ErrPasswordTooShort  = fmt.Errorf("password must be at least %d characters", minRootPasswordLength)
	ErrPasswordNotHashed = fmt.Errorf("password hashing produced no output")
)

// ValidateHostname reports whether s is usable as the system hostname.
func ValidateHostname(s string) error {
	if !hostnamePattern.MatchString(s) {
		return ErrInvalidHostname
	}
	return nil
}

// ValidateDomain reports whether s is usable as the system domain.
//
// An empty string is refused here rather than treated as "leave it
// alone": the caller decides whether the field was submitted, and a
// validator that accepts "" would let one through into the rendered
// `domain=` directive.
func ValidateDomain(s string) error {
	if len(s) > maxDomainLength || !domainPattern.MatchString(s) {
		return ErrInvalidDomain
	}
	return nil
}

// ValidateTimezone reports whether s is usable as the system timezone.
func ValidateTimezone(s string) error {
	if !timezonePattern.MatchString(s) {
		return ErrInvalidTimezone
	}
	return nil
}

// SetRootPassword hashes plaintext with the system's crypt
// implementation and installs it on the root account.
//
// The hash is produced by openssl rather than in Go because it has to
// match what /etc/shadow expects, and usermod is what writes it. The
// plaintext never reaches a config file or a log line.
func (s *SystemService) SetRootPassword(ctx context.Context, plaintext string) error {
	if len(plaintext) < minRootPasswordLength {
		return ErrPasswordTooShort
	}

	out, err := netutil.RunSimple(ctx, "openssl", "passwd", "-6", plaintext)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	hash := strings.TrimSpace(out)
	// An empty hash would be installed as an empty password field,
	// which is a passwordless root account, so refuse rather than
	// proceed on a command that reported success with no output.
	if hash == "" {
		return ErrPasswordNotHashed
	}

	if _, err := netutil.Run(ctx, "usermod", "-p", hash, "root"); err != nil {
		return fmt.Errorf("set root password: %w", err)
	}
	return nil
}

// SetHostname applies the hostname to the running system. The caller
// persists it to the config; this only touches the live setting.
func (s *SystemService) SetHostname(ctx context.Context, hostname string) error {
	if err := ValidateHostname(hostname); err != nil {
		return err
	}
	if _, err := netutil.Run(ctx, "hostnamectl", "set-hostname", hostname); err != nil {
		return fmt.Errorf("set hostname: %w", err)
	}
	return nil
}

// SetTimezone applies the timezone to the running system.
func (s *SystemService) SetTimezone(ctx context.Context, tz string) error {
	if err := ValidateTimezone(tz); err != nil {
		return err
	}
	if _, err := netutil.Run(ctx, "timedatectl", "set-timezone", tz); err != nil {
		return fmt.Errorf("set timezone: %w", err)
	}
	return nil
}

// Reboot restarts the machine.
func (s *SystemService) Reboot(ctx context.Context) error {
	if _, err := netutil.Run(ctx, "systemctl", "reboot"); err != nil {
		return fmt.Errorf("reboot: %w", err)
	}
	return nil
}
