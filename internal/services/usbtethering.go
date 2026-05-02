package services

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

type USBTetheringService struct {
	cfg    *config.Config
	mu     sync.RWMutex
	active bool
}

func NewUSBTetheringService(cfg *config.Config) *USBTetheringService {
	return &USBTetheringService{cfg: cfg}
}

type USBTetheringStatus struct {
	Enabled        bool
	PhoneConnected bool
	ActiveWAN      bool
	Interface      string
	IP             string
}

func (s *USBTetheringService) Status(ctx context.Context) (*USBTetheringStatus, error) {
	status := &USBTetheringStatus{
		Enabled:   s.cfg.USBTether.Enabled,
		Interface: s.cfg.USBTether.Interface,
	}

	if status.Interface == "" {
		status.Interface = "usb0"
	}

	state, err := netutil.GetInterfaceState(status.Interface)
	if err == nil && state == "up" {
		status.PhoneConnected = true

		addrs, _ := netutil.GetInterfaceAddresses(status.Interface)
		for _, addr := range addrs {
			if len(addr) > 0 {
				status.IP = addr
				break
			}
		}
	}

	s.mu.RLock()
	status.ActiveWAN = s.active
	s.mu.RUnlock()

	return status, nil
}

func (s *USBTetheringService) Activate(ctx context.Context) error {
	iface := s.cfg.USBTether.Interface
	if iface == "" {
		iface = "usb0"
	}

	state, err := netutil.GetInterfaceState(iface)
	if err != nil {
		return fmt.Errorf("USB interface %s not found (phone not connected?)", iface)
	}
	if state != "up" {
		_, err = netutil.Run(ctx, "ip", "link", "set", iface, "up")
		if err != nil {
			return fmt.Errorf("bring up %s: %w", iface, err)
		}
	}

	_, err = netutil.Run(ctx, "dhclient", "-1", "-v", iface)
	if err != nil {
		return fmt.Errorf("dhclient on %s: %w", iface, err)
	}

	metric := s.cfg.USBTether.Metric
	if metric == 0 {
		metric = 100
	}

	_, err = netutil.Run(ctx, "ip", "route", "replace", "default",
		"dev", iface, "metric", fmt.Sprintf("%d", metric))
	if err != nil {
		return fmt.Errorf("set USB default route: %w", err)
	}

	if s.cfg.USBTether.NAT {
		_, err = netutil.Run(ctx, "nft", "add", "rule", "ip", "nat", "postrouting",
			"oifname", iface, "masquerade")
		if err != nil {
			log.Printf("USB NAT masquerade failed: %v", err)
		}
	}

	s.mu.Lock()
	s.active = true
	s.mu.Unlock()

	log.Printf("USB tethering activated on %s (metric=%d)", iface, metric)
	return nil
}

func (s *USBTetheringService) Deactivate(ctx context.Context) error {
	iface := s.cfg.USBTether.Interface
	if iface == "" {
		iface = "usb0"
	}

	netutil.Run(ctx, "ip", "route", "del", "default", "dev", iface)
	netutil.Run(ctx, "dhclient", "-r", iface)

	s.mu.Lock()
	s.active = false
	s.mu.Unlock()

	log.Printf("USB tethering deactivated on %s", iface)
	return nil
}

func (s *USBTetheringService) IsActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

func (s *USBTetheringService) IsPhoneConnected() bool {
	iface := s.cfg.USBTether.Interface
	if iface == "" {
		iface = "usb0"
	}
	state, err := netutil.GetInterfaceState(iface)
	return err == nil && state == "up"
}
