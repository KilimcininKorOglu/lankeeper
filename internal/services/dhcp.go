package services

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
)

type DHCPService struct {
	cfg *config.Config
}

func NewDHCPService(cfg *config.Config) *DHCPService {
	return &DHCPService{cfg: cfg}
}

type dnsmasqTemplateData struct {
	LANDevice      string
	RangeStart     string
	RangeEnd       string
	LeaseTime      string
	Gateway        string
	Gateway6       string
	DNSServer      string
	Domain         string
	StaticLeases   []config.StaticLease
	IPv6Enabled    bool
	ULAPrefix      string
	ULARange       string
	RAInterval     int
	VLANDHCPRanges []vlanDHCPRange
}

type vlanDHCPRange struct {
	Device     string
	RangeStart string
	RangeEnd   string
	LeaseTime  string
	Gateway    string
	DNSServer  string
}

// RenderConfig returns the rendered dnsmasq.conf as a string. Pure
// computation — no I/O. Use RenderToDisk to write the result to /etc.
func (s *DHCPService) RenderConfig() (string, error) {
	tmpl, err := template.ParseFiles("configs/sysconf/dnsmasq.conf.tmpl")
	if err != nil {
		return "", fmt.Errorf("parse dnsmasq template: %w", err)
	}

	var lanDevice string
	for _, iface := range s.cfg.Interfaces {
		if iface.Role == "lan" {
			lanDevice = iface.Device
			break
		}
	}

	data := dnsmasqTemplateData{
		LANDevice:    lanDevice,
		RangeStart:   s.cfg.DHCP.RangeStart,
		RangeEnd:     s.cfg.DHCP.RangeEnd,
		LeaseTime:    s.cfg.DHCP.LeaseTime,
		Gateway:      s.cfg.DHCP.Gateway,
		DNSServer:    s.cfg.DHCP.DNSServer,
		Domain:       s.cfg.System.Domain,
		StaticLeases: s.cfg.DHCP.StaticLeases,
		IPv6Enabled:  s.cfg.IPv6.Enabled != "off",
		RAInterval:   s.cfg.IPv6.LAN.RAInterval,
	}

	if data.LeaseTime == "" {
		data.LeaseTime = "12h"
	}
	if data.Gateway == "" {
		data.Gateway = "10.10.10.1"
	}
	if data.DNSServer == "" {
		data.DNSServer = data.Gateway
	}
	if data.Domain == "" {
		data.Domain = "lan"
	}
	if data.RAInterval == 0 {
		data.RAInterval = 60
	}

	if s.cfg.IPv6.LAN.ULA.Enabled {
		data.ULAPrefix = s.cfg.IPv6.LAN.ULA.Prefix
	}

	for _, vlan := range s.cfg.VLANs {
		if vlan.DHCP.Enabled && vlan.Address != "" {
			var parentDev string
			for _, iface := range s.cfg.Interfaces {
				if iface.ID == vlan.Parent {
					parentDev = iface.Device
					break
				}
			}
			if parentDev != "" {
				data.VLANDHCPRanges = append(data.VLANDHCPRanges, vlanDHCPRange{
					Device:    fmt.Sprintf("%s.%d", parentDev, vlan.VID),
					Gateway:   subnetFromCIDR(vlan.Address),
					DNSServer: subnetFromCIDR(vlan.Address),
					LeaseTime: data.LeaseTime,
				})
			}
		}
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render dnsmasq.conf: %w", err)
	}

	return buf.String(), nil
}

type Lease struct {
	Expiry   time.Time
	MAC      string
	IP       string
	Hostname string
	Active   bool
}

func (s *DHCPService) GetLeases() ([]Lease, error) {
	return ParseLeaseFile("/var/lib/misc/dnsmasq.leases")
}

func ParseLeaseFile(path string) ([]Lease, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open lease file: %w", err)
	}
	defer f.Close()

	var leases []Lease
	now := time.Now()
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		expiry, _ := strconv.ParseInt(fields[0], 10, 64)
		lease := Lease{
			Expiry:   time.Unix(expiry, 0),
			MAC:      fields[1],
			IP:       fields[2],
			Hostname: fields[3],
			Active:   expiry == 0 || time.Unix(expiry, 0).After(now),
		}

		if lease.Hostname == "*" {
			lease.Hostname = ""
		}

		leases = append(leases, lease)
	}

	return leases, scanner.Err()
}

func ParseLeaseData(data string) []Lease {
	var leases []Lease
	now := time.Now()

	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		expiry, _ := strconv.ParseInt(fields[0], 10, 64)
		lease := Lease{
			Expiry:   time.Unix(expiry, 0),
			MAC:      fields[1],
			IP:       fields[2],
			Hostname: fields[3],
			Active:   expiry == 0 || time.Unix(expiry, 0).After(now),
		}
		if lease.Hostname == "*" {
			lease.Hostname = ""
		}
		leases = append(leases, lease)
	}

	return leases
}

func (s *DHCPService) Reload(ctx context.Context) error {
	_, err := netutil.Run(ctx, "killall", "-HUP", "dnsmasq")
	if err != nil {
		return fmt.Errorf("reload dnsmasq: %w", err)
	}
	return nil
}

// RenderToDisk renders /etc/dnsmasq.conf without reloading. Suitable for
// install-time invocation.
func (s *DHCPService) RenderToDisk(ctx context.Context) error {
	rendered, err := s.RenderConfig()
	if err != nil {
		return err
	}
	return netutil.WriteFile("/etc/dnsmasq.conf", []byte(rendered), 0o644)
}

// ApplyConfig renders to disk and reloads dnsmasq. Use at runtime.
func (s *DHCPService) ApplyConfig(ctx context.Context) error {
	if err := s.RenderToDisk(ctx); err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *DHCPService) GetStaticLeases() []config.StaticLease {
	return s.cfg.DHCP.StaticLeases
}

func (s *DHCPService) persist() error {
	return s.cfg.SaveToFile()
}

func (s *DHCPService) AddStaticLease(mac, ip, hostname string) error {
	for _, l := range s.cfg.DHCP.StaticLeases {
		if strings.EqualFold(l.MAC, mac) {
			return fmt.Errorf("MAC address %s already has a static lease", mac)
		}
		if l.IP == ip {
			return fmt.Errorf("IP address %s already reserved", ip)
		}
	}
	s.cfg.DHCP.StaticLeases = append(s.cfg.DHCP.StaticLeases, config.StaticLease{
		MAC:      mac,
		IP:       ip,
		Hostname: hostname,
	})
	return s.persist()
}

func (s *DHCPService) RemoveStaticLease(index int) error {
	if index < 0 || index >= len(s.cfg.DHCP.StaticLeases) {
		return fmt.Errorf("invalid static lease index: %d", index)
	}
	s.cfg.DHCP.StaticLeases = append(
		s.cfg.DHCP.StaticLeases[:index],
		s.cfg.DHCP.StaticLeases[index+1:]...,
	)
	return s.persist()
}

func (s *DHCPService) GetDeviceList() []DeviceInfo {
	leases, _ := s.GetLeases()
	devices := make([]DeviceInfo, 0, len(leases))
	for _, l := range leases {
		if l.Active {
			devices = append(devices, DeviceInfo{
				MAC:      l.MAC,
				IP:       l.IP,
				Hostname: l.Hostname,
			})
		}
	}
	return devices
}

func (s *DHCPService) RebuildDNSRecords(ctx context.Context, domain string) error {
	netutil.Run(ctx, "unbound-control", "flush_zone", domain)

	leases, _ := s.GetLeases()
	staticLeases := s.cfg.DHCP.StaticLeases

	var allEntries []struct{ hostname, ip string }

	for _, l := range leases {
		if l.Hostname != "" && l.Active {
			allEntries = append(allEntries, struct{ hostname, ip string }{l.Hostname, l.IP})
		}
	}
	for _, sl := range staticLeases {
		if sl.Hostname != "" {
			allEntries = append(allEntries, struct{ hostname, ip string }{sl.Hostname, sl.IP})
		}
	}

	for _, e := range allEntries {
		fqdn := e.hostname + "." + domain
		netutil.Run(ctx, "unbound-control", "local_data", fqdn+". 300 IN A "+e.ip)
		netutil.Run(ctx, "unbound-control", "local_data", e.hostname+". 300 IN A "+e.ip)

		parts := strings.Split(e.ip, ".")
		if len(parts) == 4 {
			ptr := parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0] + ".in-addr.arpa."
			netutil.Run(ctx, "unbound-control", "local_data", ptr+" 300 IN PTR "+fqdn+".")
		}
	}

	log.Printf("DNS records rebuilt for domain %s: %d entries", domain, len(allEntries))
	return nil
}

type DeviceInfo struct {
	MAC      string
	IP       string
	Hostname string
}
