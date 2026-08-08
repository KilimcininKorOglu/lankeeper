package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type NetworkService struct {
	cfg *config.Config
}

func NewNetworkService(cfg *config.Config) *NetworkService {
	return &NetworkService{cfg: cfg}
}

func (s *NetworkService) DetectInterfaces() ([]netutil.InterfaceInfo, error) {
	ifaces, err := netutil.DetectInterfaces()
	if err != nil {
		return nil, err
	}

	var physical []netutil.InterfaceInfo
	for _, iface := range ifaces {
		if !iface.IsVirtual {
			physical = append(physical, iface)
		}
	}
	return physical, nil
}

func (s *NetworkService) GetInterfaceStatus(name string) (*InterfaceStatus, error) {
	state, err := netutil.GetInterfaceState(name)
	if err != nil {
		return nil, err
	}

	addrs, _ := netutil.GetInterfaceAddresses(name)
	rx, tx, _ := netutil.ReadInterfaceStats(name)

	var cfgIface *config.InterfaceConfig
	for i := range s.cfg.Interfaces {
		if s.cfg.Interfaces[i].Device == name || s.cfg.Interfaces[i].ID == name {
			cfgIface = &s.cfg.Interfaces[i]
			break
		}
	}

	status := &InterfaceStatus{
		Name:      name,
		State:     state,
		Addresses: addrs,
		RxBytes:   rx,
		TxBytes:   tx,
	}

	if cfgIface != nil {
		status.Label = cfgIface.Label
		status.Role = cfgIface.Role
		status.MTU = cfgIface.MTU
	}

	return status, nil
}

type InterfaceStatus struct {
	Name      string
	Label     string
	Role      string
	State     string
	MTU       int
	Addresses []string
	RxBytes   uint64
	TxBytes   uint64
}

// Interface roles and types the config understands. Anything else
// reaches a rendered template as a value no service branches on, so the
// feature is silently inert rather than wrong in a way anyone notices.
var (
	interfaceRoles = map[string]bool{"lan": true, "wan": true}
	interfaceTypes = map[string]bool{"static": true, "dhcp-client": true, "pppoe": true}
)

var (
	// ErrLastLANInterface guards the one edit that cannot be undone
	// from the web UI: a config with no LAN interface leaves nothing
	// listening on a reachable address, so the operator would have to
	// recover over a console.
	ErrLastLANInterface = errors.New("the last LAN interface cannot be removed or reassigned")

	ErrInterfaceNotFound  = errors.New("interface not found")
	ErrInvalidRole        = errors.New("role must be lan or wan")
	ErrInvalidType        = errors.New("type must be static, dhcp-client or pppoe")
	ErrDuplicateInterface = errors.New("another interface already uses that device")
)

// ConfiguredInterfaces returns the entries the config carries, which is
// what every service renders against, as opposed to the NICs the kernel
// reports.
func (s *NetworkService) ConfiguredInterfaces() []config.InterfaceConfig {
	return s.cfg.Interfaces
}

// SetInterface adds or updates one interface entry, keyed by ID.
//
// Every field is validated here rather than in the handler, because the
// device name and address reach nftables, dnsmasq and the PPPoE peer
// templates, and those render with text/template, which performs no
// escaping of any kind.
func (s *NetworkService) SetInterface(in config.InterfaceConfig) error {
	if err := netutil.ValidateInterfaceName(in.ID); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if err := netutil.ValidateInterfaceName(in.Device); err != nil {
		return fmt.Errorf("device: %w", err)
	}
	if !interfaceRoles[in.Role] {
		return fmt.Errorf("%w: %q", ErrInvalidRole, in.Role)
	}
	if !interfaceTypes[in.Type] {
		return fmt.Errorf("%w: %q", ErrInvalidType, in.Type)
	}
	if err := netutil.ValidateRuleName(in.Label); err != nil {
		return fmt.Errorf("label: %w", err)
	}
	if in.Address != "" {
		if err := netutil.ValidateCIDR(in.Address); err != nil {
			return fmt.Errorf("address: %w", err)
		}
	}
	if in.MTU != 0 {
		if err := netutil.ValidateMTU(in.MTU); err != nil {
			return err
		}
	}

	// Two entries pointing at one device produce two conflicting
	// renderings of the same NIC, and which one wins depends on
	// iteration order.
	for _, existing := range s.cfg.Interfaces {
		if existing.ID != in.ID && existing.Device == in.Device {
			return fmt.Errorf("%w: %s", ErrDuplicateInterface, in.Device)
		}
	}

	idx := -1
	for i := range s.cfg.Interfaces {
		if s.cfg.Interfaces[i].ID == in.ID {
			idx = i
			break
		}
	}

	// Moving the only LAN interface to WAN is the same lockout as
	// deleting it, so it is refused in the same place.
	if idx >= 0 && s.cfg.Interfaces[idx].Role == "lan" && in.Role != "lan" && s.countLANExcept(in.ID) == 0 {
		return ErrLastLANInterface
	}

	if idx >= 0 {
		s.cfg.Interfaces[idx] = in
	} else {
		s.cfg.Interfaces = append(s.cfg.Interfaces, in)
	}
	return s.cfg.SaveToFile()
}

// RemoveInterface drops an entry by ID.
func (s *NetworkService) RemoveInterface(id string) error {
	idx := -1
	for i := range s.cfg.Interfaces {
		if s.cfg.Interfaces[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %s", ErrInterfaceNotFound, id)
	}

	if s.cfg.Interfaces[idx].Role == "lan" && s.countLANExcept(id) == 0 {
		return ErrLastLANInterface
	}

	s.cfg.Interfaces = append(s.cfg.Interfaces[:idx], s.cfg.Interfaces[idx+1:]...)
	return s.cfg.SaveToFile()
}

// countLANExcept counts LAN interfaces ignoring one ID, which is how
// both callers ask "would this leave any LAN behind".
func (s *NetworkService) countLANExcept(id string) int {
	n := 0
	for _, iface := range s.cfg.Interfaces {
		if iface.ID != id && iface.Role == "lan" {
			n++
		}
	}
	return n
}

func (s *NetworkService) ApplyMACClone(ctx context.Context, device, cloneMAC string) error {
	if cloneMAC == "" {
		return nil
	}
	if err := netutil.ValidateMAC(cloneMAC); err != nil {
		return err
	}

	if _, err := netutil.Run(ctx, "ip", "link", "set", device, "down"); err != nil {
		return fmt.Errorf("link down %s: %w", device, err)
	}
	_, err := netutil.Run(ctx, "ip", "link", "set", device, "address", cloneMAC)
	if err != nil {
		return fmt.Errorf("set MAC %s on %s: %w", cloneMAC, device, err)
	}
	if _, err := netutil.Run(ctx, "ip", "link", "set", device, "up"); err != nil {
		return fmt.Errorf("link up %s: %w", device, err)
	}

	log.Printf("MAC clone applied: %s → %s", device, cloneMAC)
	return nil
}

func (s *NetworkService) RestoreMACClones(ctx context.Context) {
	for _, iface := range s.cfg.Interfaces {
		if iface.CloneMAC != "" {
			if err := s.ApplyMACClone(ctx, iface.Device, iface.CloneMAC); err != nil {
				log.Printf("MAC clone restore %s: %v", iface.Device, err)
			}
		}
	}
}

func (s *NetworkService) CreateVLAN(ctx context.Context, parentDevice string, vid int, address string, mtu int) error {
	if err := netutil.ValidateVLANID(vid); err != nil {
		return err
	}

	vlanDev := fmt.Sprintf("%s.%d", parentDevice, vid)

	_, err := netutil.Run(ctx, "ip", "link", "add", "link", parentDevice,
		"name", vlanDev, "type", "vlan", "id", fmt.Sprintf("%d", vid))
	if err != nil {
		return fmt.Errorf("create VLAN %d: %w", vid, err)
	}

	if mtu > 0 {
		if _, err := netutil.Run(ctx, "ip", "link", "set", vlanDev, "mtu", fmt.Sprintf("%d", mtu)); err != nil {
			return fmt.Errorf("set mtu on %s: %w", vlanDev, err)
		}
	}

	if address != "" {
		_, err = netutil.Run(ctx, "ip", "addr", "add", address, "dev", vlanDev)
		if err != nil {
			return fmt.Errorf("assign address to VLAN %d: %w", vid, err)
		}
	}

	_, err = netutil.Run(ctx, "ip", "link", "set", vlanDev, "up")
	if err != nil {
		return fmt.Errorf("bring up VLAN %d: %w", vid, err)
	}

	return nil
}

func (s *NetworkService) DeleteVLAN(ctx context.Context, parentDevice string, vid int) error {
	vlanDev := fmt.Sprintf("%s.%d", parentDevice, vid)
	_, err := netutil.Run(ctx, "ip", "link", "delete", vlanDev)
	if err != nil {
		return fmt.Errorf("delete VLAN %s: %w", vlanDev, err)
	}
	return nil
}

func (s *NetworkService) RestoreVLANs(ctx context.Context) error {
	var errs []string
	for _, vlan := range s.cfg.VLANs {
		var parentDev string
		for _, iface := range s.cfg.Interfaces {
			if iface.ID == vlan.Parent {
				parentDev = iface.Device
				break
			}
		}
		if parentDev == "" {
			errs = append(errs, fmt.Sprintf("parent %s not found for VLAN %d", vlan.Parent, vlan.VID))
			continue
		}

		if err := s.CreateVLAN(ctx, parentDev, vlan.VID, vlan.Address, vlan.MTU); err != nil {
			errs = append(errs, fmt.Sprintf("VLAN %d: %v", vlan.VID, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("VLAN restore errors: %s", strings.Join(errs, "; "))
	}
	return nil
}
