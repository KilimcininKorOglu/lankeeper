package services

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// metricKind is the small enum we use to drive the leading
// `# TYPE` line. We only need counter+gauge today; histograms
// and summaries stay out of v1 to keep the writer simple.
type metricKind string

const (
	metricGauge   metricKind = "gauge"
	metricCounter metricKind = "counter"
)

// Write projects the snapshot onto the Prometheus text exposition
// format v0.0.4. Output is UTF-8, line-terminated by `\n` (HTTP
// 1.1 doesn't care about CRLF here), and ends with a trailing
// newline as Prometheus parsers expect.
//
// We deliberately don't use a buffered writer or temporary string:
// the snapshot is small (~150 lines, ~10 kB), so streaming is
// simpler than buffering and gives the same wall-clock cost.
func (snap MetricsSnapshot) Write(w io.Writer) error {
	snap.writeHost(w)
	snap.writeInterfaces(w)
	snap.writeDHCPAndDNS(w)
	snap.writeClients(w)
	snap.writeWireGuardPeers(w)
	snap.writeS2SPeers(w)
	snap.writeSubsystems(w)
	return nil
}

// writeHost writes the build info, uptime, CPU, memory and temperature.
func (snap MetricsSnapshot) writeHost(w io.Writer) {
	// build_info: a sentinel gauge whose only purpose is to expose the
	// version+commit pair as labels. Prometheus convention.
	if snap.BuildVersion != "" || snap.BuildCommit != "" {
		writeHelp(w, "lankeeper_build_info", "Build version and commit reported by the running binary.", metricGauge)
		writeMetric(w, "lankeeper_build_info", map[string]string{
			"version": snap.BuildVersion,
			"commit":  snap.BuildCommit,
		}, 1)
	}
	writeScalar(w, "lankeeper_uptime_seconds", "Host uptime since boot.", metricGauge, snap.UptimeSeconds)
	writeScalar(w, "lankeeper_process_start_time_seconds", "Start time of the web process since the Unix epoch, in seconds.", metricGauge, snap.ProcessStartTime)
	writeScalar(w, "lankeeper_cpu_percent", "Current system-wide CPU usage in percent (0-100).", metricGauge, snap.CPUPercent)
	writeScalar(w, "lankeeper_memory_total_bytes", "Total system memory in bytes.", metricGauge, float64(snap.MemoryTotal))
	writeScalar(w, "lankeeper_memory_used_bytes", "Resident system memory in bytes.", metricGauge, float64(snap.MemoryUsed))
	writeScalar(w, "lankeeper_temperature_celsius", "Hottest CPU/SoC sensor reading; 0 when no sensor is available.", metricGauge, snap.Temperature)
}

// writeInterfaces writes the per-interface byte counters, when there are
// interfaces.
func (snap MetricsSnapshot) writeInterfaces(w io.Writer) {
	if len(snap.Interfaces) == 0 {
		return
	}
	device := func(iface IfaceMetric) map[string]string { return map[string]string{"device": iface.Device} }
	writeFamily(w, "lankeeper_interface_rx_bytes_total", "Cumulative bytes received per OS interface.", metricCounter,
		snap.Interfaces, func(iface IfaceMetric) (map[string]string, float64) { return device(iface), float64(iface.RxBytes) })
	writeFamily(w, "lankeeper_interface_tx_bytes_total", "Cumulative bytes transmitted per OS interface.", metricCounter,
		snap.Interfaces, func(iface IfaceMetric) (map[string]string, float64) { return device(iface), float64(iface.TxBytes) })
}

// writeDHCPAndDNS writes the lease count and the Unbound counters.
func (snap MetricsSnapshot) writeDHCPAndDNS(w io.Writer) {
	if snap.DHCPCollected {
		writeScalar(w, "lankeeper_dhcp_active_leases", "Number of currently active DHCP leases.", metricGauge, float64(snap.DHCPLeases))
	}
	if snap.DNSStatsCollected {
		writeScalar(w, "lankeeper_dns_queries_total", "Total DNS queries served by Unbound.", metricCounter, float64(snap.DNSQueriesTotal))
		writeScalar(w, "lankeeper_dns_cache_hits_total", "Total DNS cache hits.", metricCounter, float64(snap.DNSCacheHitsTotal))
		writeScalar(w, "lankeeper_dns_cache_misses_total", "Total DNS cache misses.", metricCounter, float64(snap.DNSCacheMissesTotal))
	}
	if snap.DNSBlockedCollected {
		writeScalar(w, "lankeeper_dns_blocked_total", "Total DNS responses that hit a blocklist.", metricCounter, float64(snap.DNSBlockedTotal))
	}
}

// writeClients writes the per-client bandwidth series, when there are
// clients.
func (snap MetricsSnapshot) writeClients(w io.Writer) {
	if len(snap.Clients) == 0 {
		return
	}
	writeFamily(w, "lankeeper_client_rx_bytes_total", "Cumulative bytes received from each LAN client (post-NAT).", metricCounter,
		snap.Clients, func(c ClientBandwidthMetric) (map[string]string, float64) { return clientLabels(c), float64(c.RxBytes) })
	writeFamily(w, "lankeeper_client_tx_bytes_total", "Cumulative bytes transmitted to each LAN client.", metricCounter,
		snap.Clients, func(c ClientBandwidthMetric) (map[string]string, float64) { return clientLabels(c), float64(c.TxBytes) })
	writeFamily(w, "lankeeper_client_rx_bps", "Instantaneous bits per second received from each LAN client.", metricGauge,
		snap.Clients, func(c ClientBandwidthMetric) (map[string]string, float64) { return clientLabels(c), float64(c.RxBPS) })
	writeFamily(w, "lankeeper_client_tx_bps", "Instantaneous bits per second transmitted to each LAN client.", metricGauge,
		snap.Clients, func(c ClientBandwidthMetric) (map[string]string, float64) { return clientLabels(c), float64(c.TxBPS) })
}

// writeWireGuardPeers writes the road-warrior peer series, when there
// are peers.
func (snap MetricsSnapshot) writeWireGuardPeers(w io.Writer) {
	if len(snap.WGPeers) == 0 {
		return
	}
	writeFamily(w, "lankeeper_wireguard_peer_online", "1 when the WireGuard peer's last handshake is younger than 180s.", metricGauge,
		snap.WGPeers, func(p WGPeerMetric) (map[string]string, float64) { return peerLabels(p), float64(p.Online) })
	writeFamily(w, "lankeeper_wireguard_peer_handshake_age_seconds", "Seconds since the WireGuard peer's last handshake; -1 means never.", metricGauge,
		snap.WGPeers, func(p WGPeerMetric) (map[string]string, float64) { return peerLabels(p), float64(p.HandshakeAge) })
	writeFamily(w, "lankeeper_wireguard_peer_rx_bytes_total", "Cumulative bytes received from the WireGuard peer.", metricCounter,
		snap.WGPeers, func(p WGPeerMetric) (map[string]string, float64) { return peerLabels(p), float64(p.RxBytes) })
	writeFamily(w, "lankeeper_wireguard_peer_tx_bytes_total", "Cumulative bytes transmitted to the WireGuard peer.", metricCounter,
		snap.WGPeers, func(p WGPeerMetric) (map[string]string, float64) { return peerLabels(p), float64(p.TxBytes) })
}

// writeS2SPeers writes the site-to-site peer series, when there are
// peers.
func (snap MetricsSnapshot) writeS2SPeers(w io.Writer) {
	if len(snap.S2SPeers) == 0 {
		return
	}
	writeFamily(w, "lankeeper_s2s_peer_online", "1 when the site-to-site peer's last handshake is younger than 180s.", metricGauge,
		snap.S2SPeers, func(p S2SPeerMetric) (map[string]string, float64) {
			return map[string]string{"peer": p.Name}, float64(p.Online)
		})
	writeFamily(w, "lankeeper_s2s_peer_handshake_age_seconds", "Seconds since the S2S peer's last handshake; -1 means never.", metricGauge,
		snap.S2SPeers, func(p S2SPeerMetric) (map[string]string, float64) {
			return map[string]string{"peer": p.Name}, float64(p.HandshakeAge)
		})
}

// writeSubsystems writes the OpenVPN, backup, PPPoE, IPv6 and firewall
// state.
func (snap MetricsSnapshot) writeSubsystems(w io.Writer) {
	writeScalar(w, "lankeeper_openvpn_active_sessions", "Currently connected OpenVPN clients.", metricGauge, float64(snap.OpenVPNPeers))
	writeScalar(w, "lankeeper_backup_last_run_timestamp", "UNIX timestamp of the most recent backup attempt.", metricGauge, float64(snap.BackupLastRunUnix))
	writeScalar(w, "lankeeper_backup_last_status_ok", "1 when the most recent backup completed successfully.", metricGauge, float64(snap.BackupLastStatusOK))
	writeScalar(w, "lankeeper_backup_history_total", "Backup history ring-buffer size (max 50).", metricGauge, float64(snap.BackupHistorySize))
	writeScalar(w, "lankeeper_pppoe_connected", "1 when pppd is running for the configured PPPoE peer.", metricGauge, float64(snap.PPPoEConnected))
	writeScalar(w, "lankeeper_ipv6_active", "1 when an IPv6 plane (PD or 6in4) is enabled.", metricGauge, float64(snap.IPv6Active))
	writeHelp(w, "lankeeper_ipv6_mode_info", "Info-style metric carrying the configured IPv6 mode as a label.", metricGauge)
	writeMetric(w, "lankeeper_ipv6_mode_info", map[string]string{"mode": snap.IPv6Mode}, 1)
	if snap.FirewallCollected {
		writeScalar(w, "lankeeper_firewall_active", "1 when nftables ruleset is loaded.", metricGauge, float64(snap.FirewallActive))
	}
}

// writeScalar writes a family with one unlabelled sample.
func writeScalar(w io.Writer, name, help string, kind metricKind, value float64) {
	writeHelp(w, name, help, kind)
	writeMetric(w, name, nil, value)
}

// writeFamily writes a family with one sample per item.
func writeFamily[T any](w io.Writer, name, help string, kind metricKind, items []T, sample func(T) (map[string]string, float64)) {
	writeHelp(w, name, help, kind)
	for _, item := range items {
		labels, value := sample(item)
		writeMetric(w, name, labels, value)
	}
}

// peerLabels labels a WireGuard or site-to-site peer series.
func peerLabels(p WGPeerMetric) map[string]string {
	return map[string]string{"peer": p.Name}
}

// clientLabels assembles the label map for per-MAC bandwidth
// metrics in a single place so the four series stay aligned.
//
// The hashed MAC is the only label. A hostname used to sit beside it in
// the clear, which handed any LAN device a by-name inventory of its
// neighbours plus their live bandwidth over one unauthenticated
// request. Hashing it instead would only duplicate what the MAC hash
// already identifies, so it is dropped rather than obscured.
func clientLabels(c ClientBandwidthMetric) map[string]string {
	return map[string]string{"mac": c.MAC}
}

// writeHelp emits the `# HELP` and `# TYPE` lines that precede a
// metric family. We never emit them more than once per family in
// the same output - Prometheus parsers tolerate it but flag it.
func writeHelp(w io.Writer, name, help string, kind metricKind) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", name, kind)
}

// writeMetric emits a single sample line. Float values are
// serialized with %g so integers stay integer-shaped (10 not
// 10.000000) and floats stay precise enough for percentages.
func writeMetric(w io.Writer, name string, labels map[string]string, value float64) {
	if len(labels) == 0 {
		_, _ = fmt.Fprintf(w, "%s %g\n", name, value)
		return
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(labels[k]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	_, _ = fmt.Fprintf(w, "%s %g\n", b.String(), value)
}

// escapeLabelValue escapes per the Prometheus exposition spec:
//
//	\  -> \\
//	"  -> \"
//	\n -> \n  (literal two chars)
//
// All other control chars are stripped so the output is one
// physical line per metric.
func escapeLabelValue(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r', '\t':
			// Strip remaining control chars; the spec only
			// requires the three escapes above.
			continue
		default:
			if r < 0x20 {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
