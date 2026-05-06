package netutil

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type InterfaceInfo struct {
	Name      string
	MAC       string
	State     string
	Speed     string
	Driver    string
	MTU       int
	Addresses []string
	IsVirtual bool
}

func DetectInterfaces() ([]InterfaceInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	var result []InterfaceInfo
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		info := InterfaceInfo{
			Name: iface.Name,
			MAC:  iface.HardwareAddr.String(),
			MTU:  iface.MTU,
		}

		if iface.Flags&net.FlagUp != 0 {
			info.State = "up"
		} else {
			info.State = "down"
		}

		info.IsVirtual = isVirtualInterface(iface.Name)
		info.Speed = readSysfs(iface.Name, "speed")
		info.Driver = readDriverName(iface.Name)

		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				info.Addresses = append(info.Addresses, addr.String())
			}
		}

		result = append(result, info)
	}

	return result, nil
}

func GetInterfaceState(name string) (string, error) {
	state := readSysfs(name, "operstate")
	if state == "" {
		return "", fmt.Errorf("interface %s not found", name)
	}
	return state, nil
}

func GetInterfaceAddresses(name string) ([]string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("get interface %s: %w", name, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("get addrs %s: %w", name, err)
	}
	var result []string
	for _, addr := range addrs {
		result = append(result, addr.String())
	}
	return result, nil
}

func ReadInterfaceStats(name string) (rxBytes, txBytes uint64, err error) {
	path := "/proc/net/dev"
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, name+":") {
			continue
		}

		fields := strings.Fields(line[strings.Index(line, ":")+1:])
		if len(fields) < 10 {
			return 0, 0, fmt.Errorf("unexpected format for %s", name)
		}

		_, _ = fmt.Sscanf(fields[0], "%d", &rxBytes)
		_, _ = fmt.Sscanf(fields[8], "%d", &txBytes)
		return rxBytes, txBytes, nil
	}

	return 0, 0, fmt.Errorf("interface %s not found in /proc/net/dev", name)
}

func isVirtualInterface(name string) bool {
	virtPrefixes := []string{"veth", "br-", "docker", "virbr", "vxlan", "tun", "tap", "wg", "ovpn"}
	for _, prefix := range virtPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	sysPath := filepath.Join("/sys/class/net", name)
	target, err := os.Readlink(sysPath)
	if err != nil {
		return false
	}
	return strings.Contains(target, "/virtual/")
}

func readSysfs(iface, attr string) string {
	path := filepath.Join("/sys/class/net", iface, attr)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readDriverName(iface string) string {
	driverLink := filepath.Join("/sys/class/net", iface, "device", "driver")
	target, err := os.Readlink(driverLink)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}
