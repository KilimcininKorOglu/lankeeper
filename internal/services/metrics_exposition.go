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
	// build_info: a sentinel gauge whose only purpose is to expose
	// the version+commit pair as labels. Prometheus convention.
	if snap.BuildVersion != "" || snap.BuildCommit != "" {
		writeHelp(w, "lankeeper_build_info", "Build version and commit reported by the running binary.", metricGauge)
		writeMetric(w, "lankeeper_build_info", map[string]string{
			"version": snap.BuildVersion,
			"commit":  snap.BuildCommit,
		}, 1)
	}

	writeHelp(w, "lankeeper_uptime_seconds", "Process uptime since last restart.", metricGauge)
	writeMetric(w, "lankeeper_uptime_seconds", nil, snap.UptimeSeconds)

	writeHelp(w, "lankeeper_cpu_percent", "Current system-wide CPU usage in percent (0-100).", metricGauge)
	writeMetric(w, "lankeeper_cpu_percent", nil, snap.CPUPercent)

	writeHelp(w, "lankeeper_memory_total_bytes", "Total system memory in bytes.", metricGauge)
	writeMetric(w, "lankeeper_memory_total_bytes", nil, float64(snap.MemoryTotal))

	writeHelp(w, "lankeeper_memory_used_bytes", "Resident system memory in bytes.", metricGauge)
	writeMetric(w, "lankeeper_memory_used_bytes", nil, float64(snap.MemoryUsed))

	writeHelp(w, "lankeeper_temperature_celsius", "Hottest CPU/SoC sensor reading; 0 when no sensor is available.", metricGauge)
	writeMetric(w, "lankeeper_temperature_celsius", nil, snap.Temperature)

	if len(snap.Interfaces) > 0 {
		writeHelp(w, "lankeeper_interface_rx_bytes_total", "Cumulative bytes received per OS interface.", metricCounter)
		for _, iface := range snap.Interfaces {
			writeMetric(w, "lankeeper_interface_rx_bytes_total",
				map[string]string{"device": iface.Device}, float64(iface.RxBytes))
		}
		writeHelp(w, "lankeeper_interface_tx_bytes_total", "Cumulative bytes transmitted per OS interface.", metricCounter)
		for _, iface := range snap.Interfaces {
			writeMetric(w, "lankeeper_interface_tx_bytes_total",
				map[string]string{"device": iface.Device}, float64(iface.TxBytes))
		}
	}

	writeHelp(w, "lankeeper_dhcp_active_leases", "Number of currently active DHCP leases.", metricGauge)
	writeMetric(w, "lankeeper_dhcp_active_leases", nil, float64(snap.DHCPLeases))

	writeHelp(w, "lankeeper_dns_queries_total", "Total DNS queries served by Unbound.", metricCounter)
	writeMetric(w, "lankeeper_dns_queries_total", nil, float64(snap.DNSQueriesTotal))

	writeHelp(w, "lankeeper_dns_cache_hits_total", "Total DNS cache hits.", metricCounter)
	writeMetric(w, "lankeeper_dns_cache_hits_total", nil, float64(snap.DNSCacheHitsTotal))

	writeHelp(w, "lankeeper_dns_cache_misses_total", "Total DNS cache misses.", metricCounter)
	writeMetric(w, "lankeeper_dns_cache_misses_total", nil, float64(snap.DNSCacheMissesTotal))

	writeHelp(w, "lankeeper_dns_blocked_total", "Total DNS responses that hit a blocklist.", metricCounter)
	writeMetric(w, "lankeeper_dns_blocked_total", nil, float64(snap.DNSBlockedTotal))

	if len(snap.Clients) > 0 {
		writeHelp(w, "lankeeper_client_rx_bytes_total", "Cumulative bytes received from each LAN client (post-NAT).", metricCounter)
		for _, c := range snap.Clients {
			writeMetric(w, "lankeeper_client_rx_bytes_total", clientLabels(c), float64(c.RxBytes))
		}
		writeHelp(w, "lankeeper_client_tx_bytes_total", "Cumulative bytes transmitted to each LAN client.", metricCounter)
		for _, c := range snap.Clients {
			writeMetric(w, "lankeeper_client_tx_bytes_total", clientLabels(c), float64(c.TxBytes))
		}
		writeHelp(w, "lankeeper_client_rx_bps", "Instantaneous bytes-per-second received from each LAN client.", metricGauge)
		for _, c := range snap.Clients {
			writeMetric(w, "lankeeper_client_rx_bps", clientLabels(c), float64(c.RxBPS))
		}
		writeHelp(w, "lankeeper_client_tx_bps", "Instantaneous bytes-per-second transmitted to each LAN client.", metricGauge)
		for _, c := range snap.Clients {
			writeMetric(w, "lankeeper_client_tx_bps", clientLabels(c), float64(c.TxBPS))
		}
	}

	if len(snap.WGPeers) > 0 {
		writeHelp(w, "lankeeper_wireguard_peer_online", "1 when the WireGuard peer's last handshake is younger than 180s.", metricGauge)
		for _, p := range snap.WGPeers {
			writeMetric(w, "lankeeper_wireguard_peer_online",
				map[string]string{"peer": p.Name}, float64(p.Online))
		}
		writeHelp(w, "lankeeper_wireguard_peer_handshake_age_seconds", "Seconds since the WireGuard peer's last handshake; -1 means never.", metricGauge)
		for _, p := range snap.WGPeers {
			writeMetric(w, "lankeeper_wireguard_peer_handshake_age_seconds",
				map[string]string{"peer": p.Name}, float64(p.HandshakeAge))
		}
		writeHelp(w, "lankeeper_wireguard_peer_rx_bytes_total", "Cumulative bytes received from the WireGuard peer.", metricCounter)
		for _, p := range snap.WGPeers {
			writeMetric(w, "lankeeper_wireguard_peer_rx_bytes_total",
				map[string]string{"peer": p.Name}, float64(p.RxBytes))
		}
		writeHelp(w, "lankeeper_wireguard_peer_tx_bytes_total", "Cumulative bytes transmitted to the WireGuard peer.", metricCounter)
		for _, p := range snap.WGPeers {
			writeMetric(w, "lankeeper_wireguard_peer_tx_bytes_total",
				map[string]string{"peer": p.Name}, float64(p.TxBytes))
		}
	}

	if len(snap.S2SPeers) > 0 {
		writeHelp(w, "lankeeper_s2s_peer_online", "1 when the site-to-site peer's last handshake is younger than 180s.", metricGauge)
		for _, p := range snap.S2SPeers {
			writeMetric(w, "lankeeper_s2s_peer_online",
				map[string]string{"peer": p.Name}, float64(p.Online))
		}
		writeHelp(w, "lankeeper_s2s_peer_handshake_age_seconds", "Seconds since the S2S peer's last handshake; -1 means never.", metricGauge)
		for _, p := range snap.S2SPeers {
			writeMetric(w, "lankeeper_s2s_peer_handshake_age_seconds",
				map[string]string{"peer": p.Name}, float64(p.HandshakeAge))
		}
	}

	writeHelp(w, "lankeeper_openvpn_active_sessions", "Currently connected OpenVPN clients.", metricGauge)
	writeMetric(w, "lankeeper_openvpn_active_sessions", nil, float64(snap.OpenVPNPeers))

	writeHelp(w, "lankeeper_backup_last_run_timestamp", "UNIX timestamp of the most recent backup attempt.", metricGauge)
	writeMetric(w, "lankeeper_backup_last_run_timestamp", nil, float64(snap.BackupLastRunUnix))

	writeHelp(w, "lankeeper_backup_last_status_ok", "1 when the most recent backup completed successfully.", metricGauge)
	writeMetric(w, "lankeeper_backup_last_status_ok", nil, float64(snap.BackupLastStatusOK))

	writeHelp(w, "lankeeper_backup_history_total", "Backup history ring-buffer size (max 50).", metricGauge)
	writeMetric(w, "lankeeper_backup_history_total", nil, float64(snap.BackupHistorySize))

	writeHelp(w, "lankeeper_pppoe_connected", "1 when pppd is running for the configured PPPoE peer.", metricGauge)
	writeMetric(w, "lankeeper_pppoe_connected", nil, float64(snap.PPPoEConnected))

	writeHelp(w, "lankeeper_ipv6_active", "1 when an IPv6 plane (PD or 6in4) is enabled.", metricGauge)
	writeMetric(w, "lankeeper_ipv6_active", nil, float64(snap.IPv6Active))

	writeHelp(w, "lankeeper_ipv6_mode_info", "Info-style metric carrying the configured IPv6 mode as a label.", metricGauge)
	writeMetric(w, "lankeeper_ipv6_mode_info", map[string]string{"mode": snap.IPv6Mode}, 1)

	writeHelp(w, "lankeeper_firewall_active", "1 when nftables ruleset is loaded.", metricGauge)
	writeMetric(w, "lankeeper_firewall_active", nil, float64(snap.FirewallActive))

	return nil
}

// clientLabels assembles the label map for per-MAC bandwidth
// metrics in a single place so the four series stay aligned.
func clientLabels(c ClientBandwidthMetric) map[string]string {
	host := c.Hostname
	if host == "" {
		host = "unknown"
	}
	return map[string]string{
		"mac":      c.MAC,
		"hostname": host,
	}
}

// writeHelp emits the `# HELP` and `# TYPE` lines that precede a
// metric family. We never emit them more than once per family in
// the same output - Prometheus parsers tolerate it but flag it.
func writeHelp(w io.Writer, name, help string, kind metricKind) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s %s\n", name, kind)
}

// writeMetric emits a single sample line. Float values are
// serialized with %g so integers stay integer-shaped (10 not
// 10.000000) and floats stay precise enough for percentages.
func writeMetric(w io.Writer, name string, labels map[string]string, value float64) {
	if len(labels) == 0 {
		fmt.Fprintf(w, "%s %g\n", name, value)
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
	fmt.Fprintf(w, "%s %g\n", b.String(), value)
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
