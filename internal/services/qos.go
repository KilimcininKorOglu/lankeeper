package services

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type QoSService struct {
	cfg *config.Config
	mu  sync.RWMutex
}

func NewQoSService(cfg *config.Config) *QoSService {
	return &QoSService{cfg: cfg}
}

type QoSStatus struct {
	Enabled           bool
	Profile           string
	UploadKbps        int
	DownloadKbps      int
	CongestionControl string
	EgressActive      bool
	IngressActive     bool
}

func (s *QoSService) Status(ctx context.Context) (*QoSStatus, error) {
	status := &QoSStatus{
		Enabled:           s.cfg.QoS.Enabled,
		Profile:           s.cfg.QoS.Profile,
		UploadKbps:        s.cfg.QoS.UploadKbps,
		DownloadKbps:      s.cfg.QoS.DownloadKbps,
		CongestionControl: s.cfg.QoS.CongestionControl,
	}

	out, err := netutil.RunSimple(ctx, "tc", "qdisc", "show", "dev", s.wanDevice())
	if err == nil {
		if contains(out, "cake") || contains(out, "fq_codel") {
			status.EgressActive = true
		}
	}

	out, err = netutil.RunSimple(ctx, "tc", "qdisc", "show", "dev", "ifb0")
	if err == nil {
		if contains(out, "cake") || contains(out, "fq_codel") {
			status.IngressActive = true
		}
	}

	return status, nil
}

func (s *QoSService) Apply(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.clear(ctx); err != nil {
		log.Printf("qos clear before apply: %v", err)
	}

	if !s.cfg.QoS.Enabled || s.cfg.QoS.Profile == "none" {
		return nil
	}

	if err := s.setCongestionControl(ctx); err != nil {
		return fmt.Errorf("set congestion control: %w", err)
	}

	wanDev := s.wanDevice()

	switch s.cfg.QoS.Profile {
	case "cake":
		if err := s.applyCake(ctx, wanDev); err != nil {
			return err
		}
	case "fq_codel":
		if err := s.applyFqCodel(ctx, wanDev); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown QoS profile: %s", s.cfg.QoS.Profile)
	}

	log.Printf("QoS applied: profile=%s, up=%dkbps, down=%dkbps, cc=%s",
		s.cfg.QoS.Profile, s.cfg.QoS.UploadKbps, s.cfg.QoS.DownloadKbps, s.cfg.QoS.CongestionControl)
	return nil
}

func (s *QoSService) Clear(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clear(ctx)
}

func (s *QoSService) clear(ctx context.Context) error {
	wanDev := s.wanDevice()
	// Best-effort cleanup; missing qdiscs/links are not errors.
	_, _ = netutil.Run(ctx, "tc", "qdisc", "del", "dev", wanDev, "root")
	_, _ = netutil.Run(ctx, "tc", "qdisc", "del", "dev", "ifb0", "root")
	_, _ = netutil.Run(ctx, "ip", "link", "set", "ifb0", "down")
	_, _ = netutil.Run(ctx, "ip", "link", "del", "ifb0")
	return nil
}

func (s *QoSService) applyCake(ctx context.Context, wanDev string) error {
	upload := s.cfg.QoS.UploadKbps
	download := s.cfg.QoS.DownloadKbps

	_, err := netutil.Run(ctx, "tc", "qdisc", "replace", "dev", wanDev, "root",
		"cake", "bandwidth", fmt.Sprintf("%dkbit", upload),
		"besteffort", "overhead", "44", "mpu", "84", "nat", "wash")
	if err != nil {
		return fmt.Errorf("cake egress: %w", err)
	}

	// ifb0 may already exist from a previous apply; ignore the error
	// from the add and treat the set-up as the authoritative step.
	_, _ = netutil.Run(ctx, "ip", "link", "add", "ifb0", "type", "ifb")
	if _, err := netutil.Run(ctx, "ip", "link", "set", "ifb0", "up"); err != nil {
		return fmt.Errorf("ifb0 up: %w", err)
	}

	_, err = netutil.Run(ctx, "tc", "qdisc", "replace", "dev", wanDev, "handle", "ffff:",
		"ingress")
	if err != nil {
		return fmt.Errorf("ingress qdisc: %w", err)
	}

	_, err = netutil.Run(ctx, "tc", "filter", "add", "dev", wanDev, "parent", "ffff:",
		"protocol", "all", "u32", "match", "u32", "0", "0",
		"action", "mirred", "egress", "redirect", "dev", "ifb0")
	if err != nil {
		return fmt.Errorf("ingress filter: %w", err)
	}

	_, err = netutil.Run(ctx, "tc", "qdisc", "replace", "dev", "ifb0", "root",
		"cake", "bandwidth", fmt.Sprintf("%dkbit", download),
		"besteffort", "wash", "ingress", "overhead", "44", "mpu", "84")
	if err != nil {
		return fmt.Errorf("cake ingress: %w", err)
	}

	return nil
}

func (s *QoSService) applyFqCodel(ctx context.Context, wanDev string) error {
	_, err := netutil.Run(ctx, "tc", "qdisc", "replace", "dev", wanDev, "root", "fq_codel")
	if err != nil {
		return fmt.Errorf("fq_codel: %w", err)
	}
	return nil
}

func (s *QoSService) setCongestionControl(ctx context.Context) error {
	cc := s.cfg.QoS.CongestionControl
	if cc == "" {
		cc = "bbr"
	}

	if cc == "bbr" {
		if _, err := netutil.Run(ctx, "sysctl", "-w", "net.core.default_qdisc=fq"); err != nil {
			log.Printf("qos: set default_qdisc=fq: %v", err)
		}
	}

	_, err := netutil.Run(ctx, "sysctl", "-w",
		fmt.Sprintf("net.ipv4.tcp_congestion_control=%s", cc))
	return err
}

func (s *QoSService) wanDevice() string {
	for _, iface := range s.cfg.Interfaces {
		if iface.Role == "wan" && iface.Type == "pppoe" {
			return "ppp0"
		}
		if iface.Role == "wan" {
			return iface.Device
		}
	}
	return "ppp0"
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
