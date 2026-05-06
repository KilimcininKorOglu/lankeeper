package services

import (
	"context"
	"fmt"
	"net"
	"strings"
	"text/template"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// ntpHostChars is the conservative byte allowlist for any value
// rendered into chrony.conf as `server <X> iburst`. chrony's config
// grammar treats whitespace as a token break, so a value containing
// `\n`, ` `, `\t`, `;`, `"`, `'`, etc. allows directive injection
// (`pool.ntp.org\nallow 0.0.0.0/0` would expose the NTP server to
// the public internet). Real hostnames and IP literals only need
// alphanumerics, dot, hyphen, and `:` for IPv6.
const ntpHostChars = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"0123456789" +
	".-:"

func hasOnlyNTPHostChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(ntpHostChars, rune(s[i])) {
			return false
		}
	}
	return true
}

// validateNTPHost rejects empty values, values containing chrony
// config-grammar metacharacters, and strings that are neither valid
// IP literals nor RFC-1123-shaped hostnames. The result lands in
// chrony.conf without further escaping.
func validateNTPHost(host string) error {
	if host == "" {
		return fmt.Errorf("empty source")
	}
	if !hasOnlyNTPHostChars(host) {
		return fmt.Errorf("NTP source %q contains characters that could inject chrony directives", host)
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	// RFC-1123 hostname shape: 1-253 chars, dot-separated labels of
	// 1-63 chars each, alphanumerics + hyphen, no leading/trailing
	// hyphen on a label.
	if len(host) > 253 {
		return fmt.Errorf("NTP source %q exceeds 253 characters", host)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("NTP source %q has an invalid label", host)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("NTP source %q label cannot start or end with a hyphen", host)
		}
	}
	return nil
}

type NTPService struct {
	cfg *config.Config
}

func NewNTPService(cfg *config.Config) *NTPService {
	return &NTPService{cfg: cfg}
}

type NTPStatus struct {
	Synced     bool
	Stratum    int
	RefSource  string
	Offset     string
	Sources    []NTPSource
}

type NTPSource struct {
	State  string
	Name   string
	Stratum int
	Poll   string
	Offset string
}

func (s *NTPService) GetStatus(ctx context.Context) (*NTPStatus, error) {
	status := &NTPStatus{}

	out, err := netutil.RunSimple(ctx, "chronyc", "tracking")
	if err != nil {
		return status, nil
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Reference ID") {
			if idx := strings.Index(line, "("); idx != -1 {
				end := strings.Index(line[idx:], ")")
				if end != -1 {
					status.RefSource = line[idx+1 : idx+end]
				}
			}
		}
		if strings.HasPrefix(line, "Stratum") {
			_, _ = fmt.Sscanf(line, "Stratum : %d", &status.Stratum)
		}
		if strings.HasPrefix(line, "System time") {
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				status.Offset = parts[3] + " " + parts[4]
			}
		}
		if strings.HasPrefix(line, "Leap status") && strings.Contains(line, "Normal") {
			status.Synced = true
		}
	}

	sources, _ := s.getSources(ctx)
	status.Sources = sources

	return status, nil
}

func (s *NTPService) getSources(ctx context.Context) ([]NTPSource, error) {
	out, err := netutil.RunSimple(ctx, "chronyc", "sources")
	if err != nil {
		return nil, err
	}

	var sources []NTPSource
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 || line[0] == '=' || line[0] == '2' {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}

		src := NTPSource{
			State: string(line[0]),
			Name:  fields[1],
			Poll:  fields[3],
		}
		_, _ = fmt.Sscanf(fields[2], "%d", &src.Stratum)
		if len(fields) >= 8 {
			src.Offset = fields[7]
		}

		sources = append(sources, src)
	}

	return sources, nil
}

func (s *NTPService) RenderConfig() (string, error) {
	tmpl, err := template.ParseFiles("configs/sysconf/chrony.conf.tmpl")
	if err != nil {
		return "", fmt.Errorf("parse chrony template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, s.cfg.NTP); err != nil {
		return "", fmt.Errorf("execute chrony template: %w", err)
	}
	return buf.String(), nil
}

// RenderToDisk renders /etc/chrony/chrony.conf without reloading. Suitable
// for install-time invocation.
func (s *NTPService) RenderToDisk(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}
	if err := netutil.WriteFile("/etc/chrony/chrony.conf", []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write chrony config: %w", err)
	}
	return nil
}

func (s *NTPService) ApplyConfig(ctx context.Context) error {
	if err := s.RenderToDisk(ctx); err != nil {
		return err
	}
	_, err := netutil.Run(ctx, "systemctl", "reload", "chronyd")
	return err
}

func (s *NTPService) ForceSync(ctx context.Context) error {
	_, err := netutil.Run(ctx, "chronyc", "makestep")
	return err
}

func (s *NTPService) Reload(ctx context.Context) error {
	_, err := netutil.Run(ctx, "systemctl", "reload", "chronyd")
	return err
}

// GetConfig returns the live NTP configuration block.
func (s *NTPService) GetConfig() config.NTPConfig {
	return s.cfg.NTP
}

// AddSource appends a NTP server hostname to the client source list.
// The host is validated against the chrony-grammar allowlist so an
// authenticated operator cannot inject extra `allow`/`cmdallow`
// directives via an embedded newline.
func (s *NTPService) AddSource(host string) error {
	host = strings.TrimSpace(host)
	if err := validateNTPHost(host); err != nil {
		return err
	}
	for _, src := range s.cfg.NTP.Client.Sources {
		if strings.EqualFold(src, host) {
			return fmt.Errorf("source %s already exists", host)
		}
	}
	s.cfg.NTP.Client.Sources = append(s.cfg.NTP.Client.Sources, host)
	return s.cfg.SaveToFile()
}

// RemoveSource deletes the source at the given index.
func (s *NTPService) RemoveSource(index int) error {
	if index < 0 || index >= len(s.cfg.NTP.Client.Sources) {
		return fmt.Errorf("invalid source index: %d", index)
	}
	s.cfg.NTP.Client.Sources = append(
		s.cfg.NTP.Client.Sources[:index],
		s.cfg.NTP.Client.Sources[index+1:]...,
	)
	return s.cfg.SaveToFile()
}

// AddAllowSubnet appends a CIDR to the chrony "allow" list.
func (s *NTPService) AddAllowSubnet(cidr string) error {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return fmt.Errorf("empty subnet")
	}
	for _, sub := range s.cfg.NTP.AllowSubnets {
		if sub == cidr {
			return fmt.Errorf("subnet %s already in allow list", cidr)
		}
	}
	s.cfg.NTP.AllowSubnets = append(s.cfg.NTP.AllowSubnets, cidr)
	return s.cfg.SaveToFile()
}

// RemoveAllowSubnet deletes the subnet at the given index.
func (s *NTPService) RemoveAllowSubnet(index int) error {
	if index < 0 || index >= len(s.cfg.NTP.AllowSubnets) {
		return fmt.Errorf("invalid subnet index: %d", index)
	}
	s.cfg.NTP.AllowSubnets = append(
		s.cfg.NTP.AllowSubnets[:index],
		s.cfg.NTP.AllowSubnets[index+1:]...,
	)
	return s.cfg.SaveToFile()
}

// SaveSettings updates scalar NTP fields. The fallback hostname goes
// through the same chrony-grammar guard as AddSource so a `\nallow
// 0.0.0.0/0` payload cannot ride the fallback path into chrony.conf.
// The listen address must parse as a literal IPv4/IPv6 address —
// chrony's `bindaddress` directive is documented as IP-only, and a
// hostname-shaped value would silently disable the listener anyway.
// Stricter validation here also closes the chrony.conf
// newline-injection vector.
func (s *NTPService) SaveSettings(fallback, listenAddress string, listenPort int, serverEnabled, rtcSync bool) error {
	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		if err := validateNTPHost(fallback); err != nil {
			return err
		}
	}
	listenAddress = strings.TrimSpace(listenAddress)
	if listenAddress != "" {
		if err := netutil.ValidateIP(listenAddress); err != nil {
			return fmt.Errorf("NTP listen address: %w", err)
		}
	}
	s.cfg.NTP.Client.Fallback = fallback
	s.cfg.NTP.Server.Enabled = serverEnabled
	s.cfg.NTP.Server.ListenAddress = listenAddress
	s.cfg.NTP.Server.ListenPort = listenPort
	s.cfg.NTP.RTCSync = rtcSync
	return s.cfg.SaveToFile()
}
