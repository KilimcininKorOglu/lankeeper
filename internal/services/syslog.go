package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// allowedTLSDirs is the prefix allowlist for syslog TLS material.
// rsyslog runs as root and dereferences these paths directly, so a
// path-traversal write into the config (e.g. /etc/shadow) would
// surface that file's content via PEM-parse error logs visible on
// the /syslog page. Restricting to canonical TLS storage roots
// closes that exfiltration channel without breaking legitimate
// operator workflows.
var allowedTLSDirs = []string{
	"/etc/ssl/",
	"/etc/pki/",
	"/etc/lankeeper/",
	"/etc/rsyslog.d/",
}

// rsyslogPathChars is the conservative character set we allow inside
// any path interpolated into rsyslog.conf. The template renders these
// values inside RainerScript double-quoted strings — `"`, `(`, `)`,
// newlines, NULs, and whitespace would all close the string and
// permit injection of arbitrary directives such as
// `module(load="omprog" binary="/bin/sh")`.
//
// Real-world TLS / log paths only need alphanumerics, `/`, `.`, `_`,
// `-`, `+` and `~` (for keyring suffix on some distros). Everything
// else is rejected.
const rsyslogPathChars = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"0123456789" +
	"/._-+~"

// hasOnlyRsyslogPathChars reports whether every byte of p is in the
// conservative allowlist used for rsyslog.conf interpolation.
func hasOnlyRsyslogPathChars(p string) bool {
	for i := 0; i < len(p); i++ {
		if !strings.ContainsRune(rsyslogPathChars, rune(p[i])) {
			return false
		}
	}
	return true
}

type SyslogService struct {
	cfg *config.Config
}

func NewSyslogService(cfg *config.Config) *SyslogService {
	return &SyslogService{cfg: cfg}
}

func (s *SyslogService) RenderConfig() (string, error) {
	tmpl, err := template.ParseFiles("configs/sysconf/rsyslog.conf.tmpl")
	if err != nil {
		return "", fmt.Errorf("parse rsyslog template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, s.cfg.Syslog); err != nil {
		return "", fmt.Errorf("execute rsyslog template: %w", err)
	}
	return buf.String(), nil
}

// RenderToDisk renders /etc/rsyslog.d/50-lankeeper.conf without reloading.
// Suitable for install-time invocation.
func (s *SyslogService) RenderToDisk(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}
	confPath := "/etc/rsyslog.d/50-lankeeper.conf"
	if err := netutil.WriteFile(confPath, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write rsyslog config: %w", err)
	}
	return nil
}

func (s *SyslogService) ApplyConfig(ctx context.Context) error {
	if err := s.RenderToDisk(ctx); err != nil {
		return err
	}
	_, err := netutil.Run(ctx, "systemctl", "reload", "rsyslog")
	return err
}

func (s *SyslogService) Reload(ctx context.Context) error {
	_, err := netutil.Run(ctx, "systemctl", "reload", "rsyslog")
	return err
}

// GetConfig returns the live syslog configuration.
func (s *SyslogService) GetConfig() config.SyslogConfig {
	return s.cfg.Syslog
}

// SaveServerConfig replaces the server-side syslog config (listening as a
// remote sink for other devices) and persists. TLS cert/key/CA paths
// are validated against allowedTLSDirs so an authenticated operator
// cannot coerce rsyslog (running as root) into reading arbitrary
// files like /etc/shadow.
func (s *SyslogService) SaveServerConfig(cfg config.SyslogServerConfig) error {
	if err := validateTLSPath("tls_cert_file", cfg.TLSCertFile); err != nil {
		return err
	}
	if err := validateTLSPath("tls_key_file", cfg.TLSKeyFile); err != nil {
		return err
	}
	if err := validateTLSPath("tls_ca_file", cfg.TLSCAFile); err != nil {
		return err
	}
	if err := validateLogPath(cfg.LogPath); err != nil {
		return err
	}
	if err := validateRsyslogPort("listen_udp", cfg.ListenUDP); err != nil {
		return err
	}
	if err := validateRsyslogPort("listen_tcp", cfg.ListenTCP); err != nil {
		return err
	}
	s.cfg.Syslog.Server = cfg
	return s.cfg.SaveToFile()
}

// validateRsyslogPort enforces that a port destined for rsyslog.conf
// is a plain decimal integer in the legal port range. rsyslog
// renders the value inside a RainerScript double-quoted string
// literal (`port="{{ .Server.ListenUDP }}"`), so any character that
// could close that quote — `"`, `\n`, `(`, space, etc. — would
// allow an authenticated operator to inject sibling directives such
// as `module(load="omprog" binary="/bin/sh")`. Parsing through
// strconv.Atoi guarantees the persisted value contains digits only,
// which is safe to interpolate. Empty UDP value is allowed because
// the template gates `imudp` on `Server.Enabled` only when the
// operator wants UDP; we still require a parseable port if a
// non-empty value is supplied.
func validateRsyslogPort(field, raw string) error {
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("syslog %s must be a decimal port number, got %q", field, raw)
	}
	return netutil.ValidatePort(n)
}

// validateLogPath restricts the dynaFile root in rsyslog.conf to
// /var/log/. rsyslog runs as root and creates dynamic log files
// under this prefix; without the gate an authenticated operator
// could pivot it into /etc/cron.d, /etc/sudoers.d, /etc/profile.d
// and friends, with LAN syslog clients influencing file content for
// potential local privilege escalation.
func validateLogPath(p string) error {
	if p == "" {
		// rsyslog template falls back to a sane default when empty;
		// treat blank as "leave it unconfigured".
		return nil
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("syslog log_path must be an absolute path, got %q", p)
	}
	if !hasOnlyRsyslogPathChars(p) {
		return fmt.Errorf("syslog log_path contains characters that could escape rsyslog's quoted-string syntax; allowed: %s", rsyslogPathChars)
	}
	clean := filepath.Clean(p)
	if clean != p {
		return fmt.Errorf("syslog log_path contains traversal segments; expected %q, got %q", clean, p)
	}
	const root = "/var/log/"
	if clean != "/var/log" && !strings.HasPrefix(clean+"/", root) {
		return fmt.Errorf("syslog log_path %q must live under /var/log/", p)
	}
	return nil
}

// SaveClientConfig replaces the client-side syslog config (forwarding our
// logs to a remote collector) and persists. The CA path goes through
// the same allowlist as the server-side material.
func (s *SyslogService) SaveClientConfig(cfg config.SyslogClientConfig) error {
	if err := validateTLSPath("tls_ca_file", cfg.TLSCAFile); err != nil {
		return err
	}
	s.cfg.Syslog.Client = cfg
	return s.cfg.SaveToFile()
}

// validateTLSPath enforces that a syslog TLS path is empty (operator
// cleared the field), absolute, free of traversal segments after
// Clean, and rooted under one of allowedTLSDirs. Symlinks at the
// path are NOT resolved here — rsyslog itself follows symlinks at
// load time, so an operator-owned symlink in /etc/lankeeper pointing
// to /etc/shadow would still be a problem. The agent's file-write
// whitelist is the second layer that prevents lankeeper from
// CREATING such a symlink; this validator is the first layer that
// prevents the config from REFERENCING a path outside the allowlist.
func validateTLSPath(field, p string) error {
	if p == "" {
		// Empty value disables the field — rsyslog template emits a
		// commented-out directive, no file is opened.
		return nil
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("syslog %s must be an absolute path, got %q", field, p)
	}
	if !hasOnlyRsyslogPathChars(p) {
		return fmt.Errorf("syslog %s contains characters that could escape rsyslog's quoted-string syntax; allowed: %s", field, rsyslogPathChars)
	}
	clean := filepath.Clean(p)
	if clean != p {
		return fmt.Errorf("syslog %s contains traversal segments; expected %q, got %q", field, clean, p)
	}
	for _, dir := range allowedTLSDirs {
		if strings.HasPrefix(clean+"/", dir) || strings.HasPrefix(clean, dir) {
			return nil
		}
	}
	return fmt.Errorf("syslog %s %q must live under one of %v", field, p, allowedTLSDirs)
}

// AddFacility appends a syslog facility name to the client forwarding list.
// Validation against the allowed RFC 5424 facility names is the caller's
// responsibility.
func (s *SyslogService) AddFacility(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty facility")
	}
	for _, f := range s.cfg.Syslog.Client.Facilities {
		if strings.EqualFold(f, name) {
			return fmt.Errorf("facility %s already configured", name)
		}
	}
	s.cfg.Syslog.Client.Facilities = append(s.cfg.Syslog.Client.Facilities, name)
	return s.cfg.SaveToFile()
}

// RemoveFacility deletes the facility at the given index.
func (s *SyslogService) RemoveFacility(index int) error {
	if index < 0 || index >= len(s.cfg.Syslog.Client.Facilities) {
		return fmt.Errorf("invalid facility index: %d", index)
	}
	s.cfg.Syslog.Client.Facilities = append(
		s.cfg.Syslog.Client.Facilities[:index],
		s.cfg.Syslog.Client.Facilities[index+1:]...,
	)
	return s.cfg.SaveToFile()
}

// GetRecentLogs tails the local /var/log/syslog (best-effort).
func (s *SyslogService) GetRecentLogs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out, err := netutil.RunSimple(ctx, "tail", "-n", fmt.Sprintf("%d", limit), "/var/log/syslog")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines, nil
}

func (s *SyslogService) GetRemoteHosts(ctx context.Context) ([]string, error) {
	logPath := s.cfg.Syslog.Server.LogPath
	if logPath == "" {
		logPath = "/var/log/remote"
	}

	entries, err := os.ReadDir(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var hosts []string
	for _, e := range entries {
		if e.IsDir() {
			hosts = append(hosts, e.Name())
		}
	}
	return hosts, nil
}
