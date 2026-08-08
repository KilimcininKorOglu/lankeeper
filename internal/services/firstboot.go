package services

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// First-boot networking exists because the shipped interface names are
// a guess. configs/defaults/router.yaml names enp3s0 and enp0s25, which
// are the developer's NICs and almost certainly not the operator's, so
// on unfamiliar hardware there is no port the web UI is reachable on.
// Bridging every physical NIC and answering on 10.10.10.1 makes the UI
// reachable from any port while the operator corrects the config.
//
// The bridge carries the future WAN NIC too, so first-boot mode is an
// exposure with a definite cost: plugging the ISP line in while it is
// active puts the upstream segment on the LAN bridge. That is why it
// ends on an explicit operator action rather than lingering until
// something happens to tear it down.
//
// This lives in services rather than web because it is netutil work
// like every other service, and because the handlers package cannot
// import web without an import cycle.

const (
	firstBootBridge = "br0"
	firstBootCIDR   = "10.10.10.1/24"

	defaultFirstBootFlag = "/var/lib/lankeeper/.first-boot"
)

type FirstBootService struct {
	cfg *config.Config
}

func NewFirstBootService(cfg *config.Config) *FirstBootService {
	return &FirstBootService{cfg: cfg}
}

// flagPath resolves the marker location. The environment override
// exists for tests, which cannot write under /var/lib, and matches how
// the credential key paths are overridden.
func firstBootFlagPath() string {
	if p := os.Getenv("LANKEEPER_FIRST_BOOT_FLAG"); p != "" {
		return p
	}
	return defaultFirstBootFlag
}

// IsActive reports whether the installer left the first-boot marker and
// nothing has cleared it yet.
func (s *FirstBootService) IsActive() bool {
	_, err := os.Stat(firstBootFlagPath())
	return err == nil
}

// WANDevices returns the devices the config currently calls WAN. These
// are the ones Complete detaches, and the page shows them so the
// operator can see what the button will do before pressing it.
func (s *FirstBootService) WANDevices() []string {
	var devices []string
	for _, iface := range s.cfg.Interfaces {
		if iface.Role == "wan" && iface.Device != "" {
			devices = append(devices, iface.Device)
		}
	}
	return devices
}

// Setup enslaves every physical NIC into the bridge and gives it the
// default LAN address.
//
// Individual link failures are logged and skipped rather than aborting:
// a NIC that refuses to join still leaves the remaining ports usable,
// and giving up on the first error would leave a half-built bridge with
// no address on it at all. Only the address assignment is fatal, since
// without it nothing is reachable and the caller should say so.
func (s *FirstBootService) Setup(ctx context.Context) ([]string, error) {
	ifaces, err := netutil.DetectInterfaces()
	if err != nil {
		return nil, fmt.Errorf("detect interfaces: %w", err)
	}

	var physicalNICs []string
	for _, iface := range ifaces {
		if iface.IsVirtual || iface.Name == "lo" {
			continue
		}
		physicalNICs = append(physicalNICs, iface.Name)
	}

	if len(physicalNICs) == 0 {
		return nil, fmt.Errorf("no physical NICs found")
	}

	if _, err := netutil.Run(ctx, "ip", "link", "add", firstBootBridge, "type", "bridge"); err != nil {
		log.Printf("first-boot: bridge add: %v", err)
	}
	if _, err := netutil.Run(ctx, "ip", "link", "set", firstBootBridge, "up"); err != nil {
		log.Printf("first-boot: bridge up: %v", err)
	}

	var enslaved []string
	for _, nic := range physicalNICs {
		if _, err := netutil.Run(ctx, "ip", "addr", "flush", "dev", nic); err != nil {
			log.Printf("first-boot: addr flush %s: %v", nic, err)
		}
		if _, err := netutil.Run(ctx, "ip", "link", "set", nic, "up"); err != nil {
			log.Printf("first-boot: link up %s: %v", nic, err)
		}
		if _, err := netutil.Run(ctx, "ip", "link", "set", nic, "master", firstBootBridge); err != nil {
			log.Printf("first-boot: failed to add %s to bridge: %v", nic, err)
			continue
		}
		enslaved = append(enslaved, nic)
	}

	if _, err := netutil.Run(ctx, "ip", "addr", "add", firstBootCIDR, "dev", firstBootBridge); err != nil {
		return enslaved, fmt.Errorf("assign bridge IP: %w", err)
	}

	log.Printf("first-boot: bridge %s ready at %s with %d NICs", firstBootBridge, firstBootCIDR, len(enslaved))
	return enslaved, nil
}

// Complete ends first-boot mode: the configured WAN devices leave the
// bridge, and the marker is removed so the next start does not rebuild
// it.
//
// The bridge itself is deliberately left standing. It holds the LAN
// address the operator is almost certainly connected over, so removing
// it here would drop the session that pressed the button. It goes away
// on the next boot, when the marker is gone and Setup no longer runs.
func (s *FirstBootService) Complete(ctx context.Context) error {
	if !s.IsActive() {
		return fmt.Errorf("first-boot mode is not active")
	}

	for _, dev := range s.WANDevices() {
		// Best-effort: the device may never have joined, or may carry a
		// name that does not exist on this hardware, which is the very
		// situation first-boot mode is for.
		if _, err := netutil.Run(ctx, "ip", "link", "set", dev, "nomaster"); err != nil {
			log.Printf("first-boot: detach %s from bridge: %v", dev, err)
			continue
		}
		log.Printf("first-boot: removed %s from bridge (assigned as WAN)", dev)
	}

	if err := os.Remove(firstBootFlagPath()); err != nil {
		return fmt.Errorf("clear first-boot flag: %w", err)
	}

	log.Printf("first-boot: setup completed by operator")
	return nil
}

// RemoveBridge tears the bridge down entirely. Only safe once the
// operator is no longer reaching the router through it.
func (s *FirstBootService) RemoveBridge(ctx context.Context) {
	_, _ = netutil.Run(ctx, "ip", "link", "set", firstBootBridge, "down")
	_, _ = netutil.Run(ctx, "ip", "link", "del", firstBootBridge)
	log.Printf("first-boot: bridge %s removed", firstBootBridge)
}
