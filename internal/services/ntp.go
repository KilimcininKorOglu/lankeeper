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
			fmt.Sscanf(line, "Stratum : %d", &status.Stratum)
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
		fmt.Sscanf(fields[2], "%d", &src.Stratum)
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

func (s *NTPService) ApplyConfig(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}

	if err := os.WriteFile("/etc/chrony/chrony.conf", []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write chrony config: %w", err)
	}

	_, err = netutil.Run(ctx, "systemctl", "reload", "chronyd")
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
