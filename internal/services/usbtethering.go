package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type USBTetheringService struct {
	cfg    *config.Config
	mu     sync.RWMutex
	active bool
}

func NewUSBTetheringService(cfg *config.Config) *USBTetheringService {
	return &USBTetheringService{cfg: cfg}
}

// ErrPhoneNotConnected reports that the tethering interface is absent,
// which is an operator-correctable condition rather than a fault. The
// handler turns it into a 400 with a message naming the interface, so a
// forgotten cable does not read as a broken router.
var ErrPhoneNotConnected = errors.New("usb tethering interface is not present")

type USBTetheringStatus struct {
	Enabled        bool
	AutoFailover   bool
	PhoneConnected bool
	ActiveWAN      bool
	Interface      string
	IP             string
}

// SetEnabled records whether the failover path may be used at all.
// Turning it off does not tear down a live session: an operator who
// disables the feature while the phone is carrying traffic would
// otherwise lose the connection they are managing the router over.
// Deactivate is the explicit way to end a session.
func (s *USBTetheringService) SetEnabled(enabled bool) error {
	s.cfg.USBTether.Enabled = enabled
	return s.cfg.SaveToFile()
}

// SetAutoFailover records whether the health check chain may switch to
// USB on its own.
func (s *USBTetheringService) SetAutoFailover(enabled bool) error {
	s.cfg.USBTether.AutoFailover = enabled
	return s.cfg.SaveToFile()
}

func (s *USBTetheringService) Status(ctx context.Context) (*USBTetheringStatus, error) {
	status := &USBTetheringStatus{
		Enabled:      s.cfg.USBTether.Enabled,
		AutoFailover: s.cfg.USBTether.AutoFailover,
		Interface:    s.cfg.USBTether.Interface,
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
		return fmt.Errorf("%w: %s", ErrPhoneNotConnected, iface)
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

	// Best-effort cleanup; missing routes/leases are not errors.
	_, _ = netutil.Run(ctx, "ip", "route", "del", "default", "dev", iface)
	_, _ = netutil.Run(ctx, "dhclient", "-r", iface)

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
