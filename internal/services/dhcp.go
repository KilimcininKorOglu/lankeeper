package services

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
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
	DNSServer      string
	Domain         string
	StaticLeases   []config.StaticLease
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

	data := s.dnsmasqData()
	data.VLANDHCPRanges = s.vlanDHCPRanges(data.LeaseTime)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render dnsmasq.conf: %w", err)
	}

	return buf.String(), nil
}

// dnsmasqData fills the LAN part of the dnsmasq template data, with
// defaults for the unset fields.
func (s *DHCPService) dnsmasqData() dnsmasqTemplateData {
	gateway := cmp.Or(s.cfg.DHCP.Gateway, "10.10.10.1")
	data := dnsmasqTemplateData{
		LANDevice:    firstRoleDevice(s.cfg.Interfaces, "lan"),
		RangeStart:   s.cfg.DHCP.RangeStart,
		RangeEnd:     s.cfg.DHCP.RangeEnd,
		LeaseTime:    cmp.Or(s.cfg.DHCP.LeaseTime, "12h"),
		Gateway:      gateway,
		DNSServer:    cmp.Or(s.cfg.DHCP.DNSServer, gateway),
		Domain:       cmp.Or(s.cfg.System.Domain, "lan"),
		StaticLeases: s.cfg.DHCP.StaticLeases,
	}
	return data
}

// vlanDHCPRanges lists a DHCP range for every VLAN that serves DHCP,
// has an address and has a parent interface.
func (s *DHCPService) vlanDHCPRanges(leaseTime string) []vlanDHCPRange {
	var ranges []vlanDHCPRange
	for _, vlan := range s.cfg.VLANs {
		if !vlan.DHCP.Enabled || vlan.Address == "" {
			continue
		}
		parentDev := deviceByID(s.cfg.Interfaces, vlan.Parent)
		start, end, ok := vlanRangeBounds(vlan)
		// The router's own address on the VLAN is the gateway and the
		// resolver the clients are told about.
		routerIP, _, err := net.ParseCIDR(vlan.Address)
		if parentDev == "" || !ok || err != nil {
			continue
		}
		ranges = append(ranges, vlanDHCPRange{
			Device:     fmt.Sprintf("%s.%d", parentDev, vlan.VID),
			RangeStart: start,
			RangeEnd:   end,
			Gateway:    routerIP.String(),
			DNSServer:  routerIP.String(),
			LeaseTime:  cmp.Or(vlan.DHCP.LeaseTime, leaseTime),
		})
	}
	return ranges
}

// vlanRangeBounds returns the DHCP range for a VLAN: the one the entry
// names, or else .100 to .200 of a /24 or wider subnet, the same span
// the LAN ships with, or every host but the first of a smaller one.
// The VLAN page has no range fields, so the second case is the usual
// one. ok is false when the address yields no usable range.
func vlanRangeBounds(vlan config.VLANConfig) (start, end string, ok bool) {
	if vlan.DHCP.RangeStart != "" && vlan.DHCP.RangeEnd != "" {
		return vlan.DHCP.RangeStart, vlan.DHCP.RangeEnd, true
	}
	_, subnet, err := net.ParseCIDR(vlan.Address)
	if err != nil || subnet.IP.To4() == nil {
		return "", "", false
	}
	ones, bits := subnet.Mask.Size()
	hostBits := bits - ones
	base := binary.BigEndian.Uint32(subnet.IP.To4())
	if hostBits >= 8 {
		return uint32IP(base + 100), uint32IP(base + 200), true
	}
	if hostBits < 2 {
		return "", "", false
	}
	broadcast := base | (uint32(1)<<hostBits - 1)
	return uint32IP(base + 2), uint32IP(broadcast - 1), true
}

// uint32IP formats an IPv4 address held as a number.
func uint32IP(v uint32) string {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, v)
	return ip.String()
}

// deviceByID returns the device of the interface with the given ID, or
// "" when no interface has it.
func deviceByID(ifaces []config.InterfaceConfig, id string) string {
	for _, iface := range ifaces {
		if iface.ID == id {
			return iface.Device
		}
	}
	return ""
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
	// path is the dnsmasq lease file location, a constant.
	// #nosec G304
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

	for line := range strings.SplitSeq(data, "\n") {
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

// ErrStaticLeaseAddress reports a reservation address the router serves
// no DHCP for.
var ErrStaticLeaseAddress = errors.New("static lease address is not a host on a served subnet")

// validateStaticLease checks a reservation before it is stored. The name
// reaches dnsmasq.conf and unbound.conf through text/template, which
// escapes nothing: a newline there adds a directive a root daemon runs,
// and a space makes dnsmasq refuse the whole file. The address must be a
// host on a subnet the router serves DHCP on, because dnsmasq ignores a
// dhcp-host address outside every dhcp-range subnet while the mirrored
// DNS record would still point the name at it.
func (s *DHCPService) validateStaticLease(ip, hostname string) error {
	if hostname != "" {
		if err := ValidateHostname(hostname); err != nil {
			return err
		}
	}
	addr := net.ParseIP(ip).To4()
	if addr == nil {
		return fmt.Errorf("%w: %q is not an IPv4 address", ErrStaticLeaseAddress, ip)
	}
	for _, cidr := range s.dhcpSubnets() {
		_, subnet, err := net.ParseCIDR(cidr)
		if err != nil || !subnet.Contains(addr) {
			continue
		}
		if isNetworkOrBroadcast(addr, subnet) {
			return fmt.Errorf("%w: %s is the network or broadcast address of %s", ErrStaticLeaseAddress, ip, subnet)
		}
		return nil
	}
	return fmt.Errorf("%w: %s is outside every LAN and DHCP-enabled VLAN subnet", ErrStaticLeaseAddress, ip)
}

// dhcpSubnets lists the LAN interface and DHCP-enabled VLAN addresses.
func (s *DHCPService) dhcpSubnets() []string {
	var cidrs []string
	for _, iface := range s.cfg.Interfaces {
		if iface.Role == "lan" {
			cidrs = append(cidrs, iface.Address)
		}
	}
	for _, vlan := range s.cfg.VLANs {
		if vlan.DHCP.Enabled {
			cidrs = append(cidrs, vlan.Address)
		}
	}
	return cidrs
}

// isNetworkOrBroadcast reports whether addr is the first or last address
// of an IPv4 subnet shorter than /31.
func isNetworkOrBroadcast(addr net.IP, subnet *net.IPNet) bool {
	if ones, _ := subnet.Mask.Size(); ones >= 31 {
		return false
	}
	network := subnet.IP.To4()
	broadcast := make(net.IP, len(network))
	for i := range network {
		broadcast[i] = network[i] | ^subnet.Mask[i]
	}
	return addr.Equal(network) || addr.Equal(broadcast)
}

func (s *DHCPService) AddStaticLease(mac, ip, hostname string) error {
	if err := s.validateStaticLease(ip, hostname); err != nil {
		return err
	}
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

// ApplyDNSMirror reloads Unbound after a static lease changed its mirrored
// record. The record lives in unbound.conf as local-data, and saving it to
// router.yaml alone left Unbound serving the old name until something else
// re-rendered it.
func (s *DHCPService) ApplyDNSMirror(ctx context.Context) error {
	if s.dns == nil {
		return nil
	}
	return s.dns.ApplyConfig(ctx)
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
	leases, err := s.GetLeases()
	if err != nil {
		log.Printf("dns refresh: read leases: %v", err)
	}
	resolveDomain := cmp.Or(domain, s.cfg.System.Domain, "lan")
	if _, err := netutil.Run(ctx, "unbound-control", "flush_zone", resolveDomain); err != nil {
		log.Printf("dns refresh: flush_zone %s: %v", resolveDomain, err)
	}
	count := 0
	for _, l := range leases {
		if l.Hostname == "" || !l.Active {
			continue
		}
		injectLeaseRecords(ctx, l, resolveDomain)
		count++
	}
	log.Printf("DNS active-lease entries refreshed for %s: %d", resolveDomain, count)
	return nil
}

// injectLeaseRecords adds the runtime A records for the lease's FQDN and
// bare hostname, and the PTR record for an IPv4 address. A failure is
// logged and the next record is still tried.
func injectLeaseRecords(ctx context.Context, l Lease, domain string) {
	fqdn := l.Hostname + "." + domain
	if _, err := netutil.Run(ctx, "unbound-control", "local_data", fqdn+". 300 IN A "+l.IP); err != nil {
		log.Printf("dns refresh: local_data fqdn %s: %v", fqdn, err)
	}
	if _, err := netutil.Run(ctx, "unbound-control", "local_data", l.Hostname+". 300 IN A "+l.IP); err != nil {
		log.Printf("dns refresh: local_data hostname %s: %v", l.Hostname, err)
	}
	parts := strings.Split(l.IP, ".")
	if len(parts) != 4 {
		return
	}
	ptr := parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0] + ".in-addr.arpa."
	if _, err := netutil.Run(ctx, "unbound-control", "local_data", ptr+" 300 IN PTR "+fqdn+"."); err != nil {
		log.Printf("dns refresh: local_data ptr %s: %v", ptr, err)
	}
}

type DeviceInfo struct {
	MAC      string
	IP       string
	Hostname string
}
