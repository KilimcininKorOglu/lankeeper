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

func (s *SyslogService) ApplyConfig(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}

	confPath := "/etc/rsyslog.d/50-home-router.conf"
	if err := os.WriteFile(confPath, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write rsyslog config: %w", err)
	}

	_, err = netutil.Run(ctx, "systemctl", "reload", "rsyslog")
	return err
}

func (s *SyslogService) Reload(ctx context.Context) error {
	_, err := netutil.Run(ctx, "systemctl", "reload", "rsyslog")
	return err
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
