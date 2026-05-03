package services

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

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

// RenderToDisk renders /etc/rsyslog.d/50-home-router.conf without reloading.
// Suitable for install-time invocation.
func (s *SyslogService) RenderToDisk(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}
	confPath := "/etc/rsyslog.d/50-home-router.conf"
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
// remote sink for other devices) and persists.
func (s *SyslogService) SaveServerConfig(cfg config.SyslogServerConfig) error {
	s.cfg.Syslog.Server = cfg
	return s.cfg.SaveToFile()
}

// SaveClientConfig replaces the client-side syslog config (forwarding our
// logs to a remote collector) and persists.
func (s *SyslogService) SaveClientConfig(cfg config.SyslogClientConfig) error {
	s.cfg.Syslog.Client = cfg
	return s.cfg.SaveToFile()
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
