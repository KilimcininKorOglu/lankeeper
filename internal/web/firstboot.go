package web

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

const (
	firstBootFlag   = "/var/lib/lankeeper/.first-boot"
	firstBootBridge = "br0"
	firstBootIP     = "10.10.10.1"
	firstBootCIDR   = "10.10.10.1/24"
)

func IsFirstBoot() bool {
	_, err := os.Stat(firstBootFlag)
	return err == nil
}

func CompleteFirstBoot() error {
	return os.Remove(firstBootFlag)
}

func SetupFirstBootNetworking(ctx context.Context) ([]string, error) {
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

	for _, nic := range physicalNICs {
		if _, err := netutil.Run(ctx, "ip", "addr", "flush", "dev", nic); err != nil {
			log.Printf("first-boot: addr flush %s: %v", nic, err)
		}
		if _, err := netutil.Run(ctx, "ip", "link", "set", nic, "up"); err != nil {
			log.Printf("first-boot: link up %s: %v", nic, err)
		}
		_, err := netutil.Run(ctx, "ip", "link", "set", nic, "master", firstBootBridge)
		if err != nil {
			log.Printf("first-boot: failed to add %s to bridge: %v", nic, err)
			continue
		}
		log.Printf("first-boot: %s → %s", nic, firstBootBridge)
	}

	_, err = netutil.Run(ctx, "ip", "addr", "add", firstBootCIDR, "dev", firstBootBridge)
	if err != nil {
		return nil, fmt.Errorf("assign bridge IP: %w", err)
	}

	log.Printf("first-boot: bridge %s ready at %s with %d NICs", firstBootBridge, firstBootIP, len(physicalNICs))
	return physicalNICs, nil
}

func TeardownFirstBootBridge(ctx context.Context, wanDevices []string) {
	for _, dev := range wanDevices {
		// Best-effort: device may already be detached.
		if _, err := netutil.Run(ctx, "ip", "link", "set", dev, "nomaster"); err != nil {
			log.Printf("first-boot: detach %s from bridge: %v", dev, err)
			continue
		}
		log.Printf("first-boot: removed %s from bridge (assigned as WAN)", dev)
	}
}

func RemoveFirstBootBridge(ctx context.Context) {
	// Best-effort teardown; bridge may already be gone.
	_, _ = netutil.Run(ctx, "ip", "link", "set", firstBootBridge, "down")
	_, _ = netutil.Run(ctx, "ip", "link", "del", firstBootBridge)
	log.Printf("first-boot: bridge %s removed", firstBootBridge)
}
