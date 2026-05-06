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

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type DHCPService struct {
	cfg *config.Config
	dns *DNSService // optional; set via SetDNSService for static-lease DNS mirror
}

func NewDHCPService(cfg *config.Config) *DHCPService {
	return &DHCPService{cfg: cfg}
}

// SetDNSService wires a DNSService into the DHCP service so static-lease
// mutations automatically mirror to / clean up the corresponding
// StaticDNSRecord with Source="dhcp-static". Optional — DHCP works
// without it (no DNS mirror).
func (s *DHCPService) SetDNSService(dns *DNSService) {
	s.dns = dns
}

// staticLeaseFQDN builds the FQDN for a hostname using the configured
// system domain (default "lan").
func (s *DHCPService) staticLeaseFQDN(hostname string) string {
	domain := s.cfg.System.Domain
	if domain == "" {
		domain = "lan"
	}
	return hostname + "." + domain
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
	defer func() { _ = f.Close() }()

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
	if err := s.persist(); err != nil {
		return err
	}
	// Mirror to a persistent StaticDNSRecord so the host is resolvable
	// across unbound reloads (the runtime dhcp-script injection is
	// ephemeral).
	if s.dns != nil && hostname != "" {
		fqdn := s.staticLeaseFQDN(hostname)
		err := s.dns.AddStaticRecord(config.StaticDNSRecord{
			Name:   fqdn,
			IP:     ip,
			Source: config.DNSSourceDHCPStatic,
		})
		if err != nil {
			log.Printf("dhcp: dns mirror add %s: %v", fqdn, err)
		}
	}
	return nil
}

func (s *DHCPService) RemoveStaticLease(index int) error {
	if index < 0 || index >= len(s.cfg.DHCP.StaticLeases) {
		return fmt.Errorf("invalid static lease index: %d", index)
	}
	removed := s.cfg.DHCP.StaticLeases[index]
	s.cfg.DHCP.StaticLeases = append(
		s.cfg.DHCP.StaticLeases[:index],
		s.cfg.DHCP.StaticLeases[index+1:]...,
	)
	if err := s.persist(); err != nil {
		return err
	}
	// Drop the corresponding DHCP-mirrored DNS record (if any). User-added
	// records with the same name (Source="") are protected by the
	// Source filter on FindStaticRecordIndexBySource.
	if s.dns != nil && removed.Hostname != "" {
		fqdn := s.staticLeaseFQDN(removed.Hostname)
		if idx := s.dns.FindStaticRecordIndexBySource(config.DNSSourceDHCPStatic, fqdn); idx >= 0 {
			if err := s.dns.RemoveStaticRecord(idx); err != nil {
				log.Printf("dhcp: dns mirror remove %s: %v", fqdn, err)
			}
		}
	}
	return nil
}

// SyncStaticDNSRecords rebuilds all Source="dhcp-static" StaticDNSRecord
// entries from the current static lease list. Idempotent. Triggered when
// the system domain changes (FQDNs need to be rewritten under the new
// suffix) or any time wholesale re-mirror is desired.
func (s *DHCPService) SyncStaticDNSRecords(ctx context.Context) error {
	if s.dns == nil {
		return nil
	}
	// Strip every existing dhcp-static record (cleanup).
	for {
		all := s.dns.GetStaticRecords()
		idx := -1
		for i, r := range all {
			if r.Source == config.DNSSourceDHCPStatic {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		if err := s.dns.RemoveStaticRecord(idx); err != nil {
			return fmt.Errorf("strip dns mirror: %w", err)
		}
	}
	// Re-add from the current static lease list.
	for _, lease := range s.cfg.DHCP.StaticLeases {
		if lease.Hostname == "" {
			continue
		}
		fqdn := s.staticLeaseFQDN(lease.Hostname)
		err := s.dns.AddStaticRecord(config.StaticDNSRecord{
			Name:   fqdn,
			IP:     lease.IP,
			Source: config.DNSSourceDHCPStatic,
		})
		if err != nil {
			log.Printf("dhcp: dns sync add %s: %v", fqdn, err)
		}
	}
	return s.dns.ApplyConfig(ctx)
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

// RebuildDNSRecords re-syncs DHCP-mirrored static records and re-injects
// runtime entries for currently active dynamic leases. Static leases now
// flow through the persistent StaticDNSRecord pipeline (template-rendered
// + reload-safe); active leases stay ephemeral via unbound-control as
// before. The `domain` argument is accepted for backward compatibility
// with system handler call sites; the actual domain comes from
// s.cfg.System.Domain via staticLeaseFQDN.
func (s *DHCPService) RebuildDNSRecords(ctx context.Context, domain string) error {
	if err := s.SyncStaticDNSRecords(ctx); err != nil {
		log.Printf("dhcp: SyncStaticDNSRecords: %v", err)
	}

	// Active leases: ephemeral runtime injection (no persistence).
	leases, _ := s.GetLeases()
	resolveDomain := domain
	if resolveDomain == "" {
		resolveDomain = s.cfg.System.Domain
	}
	if resolveDomain == "" {
		resolveDomain = "lan"
	}
	if _, err := netutil.Run(ctx, "unbound-control", "flush_zone", resolveDomain); err != nil {
		log.Printf("dns refresh: flush_zone %s: %v", resolveDomain, err)
	}
	count := 0
	for _, l := range leases {
		if l.Hostname == "" || !l.Active {
			continue
		}
		fqdn := l.Hostname + "." + resolveDomain
		if _, err := netutil.Run(ctx, "unbound-control", "local_data", fqdn+". 300 IN A "+l.IP); err != nil {
			log.Printf("dns refresh: local_data fqdn %s: %v", fqdn, err)
		}
		if _, err := netutil.Run(ctx, "unbound-control", "local_data", l.Hostname+". 300 IN A "+l.IP); err != nil {
			log.Printf("dns refresh: local_data hostname %s: %v", l.Hostname, err)
		}
		parts := strings.Split(l.IP, ".")
		if len(parts) == 4 {
			ptr := parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0] + ".in-addr.arpa."
			if _, err := netutil.Run(ctx, "unbound-control", "local_data", ptr+" 300 IN PTR "+fqdn+"."); err != nil {
				log.Printf("dns refresh: local_data ptr %s: %v", ptr, err)
			}
		}
		count++
	}
	log.Printf("DNS active-lease entries refreshed for %s: %d", resolveDomain, count)
	return nil
}

type DeviceInfo struct {
	MAC      string
	IP       string
	Hostname string
}
