package services

import (
	"bytes"
	"strings"
	"testing"
)

func TestExpositionSnapshotShapesAreStable(t *testing.T) {
	snap := MetricsSnapshot{
		BuildVersion:      "0.5.0-test",
		BuildCommit:       "deadbeef",
		UptimeSeconds:     12345,
		CPUPercent:        17.5,
		MemoryTotal:       8 << 30,
		MemoryUsed:        2 << 30,
		Temperature:       42.5,
		DHCPLeases:        12,
		DNSQueriesTotal:   1000,
		DNSCacheHitsTotal: 750,
		Interfaces: []IfaceMetric{
			{Device: "eth0", RxBytes: 100, TxBytes: 200},
			{Device: "wlan0", RxBytes: 0, TxBytes: 0},
		},
		Clients: []ClientBandwidthMetric{
			{MAC: "abcdef01", Hostname: "laptop", RxBytes: 50, TxBytes: 60, RxBPS: 1, TxBPS: 2},
		},
		WGPeers: []WGPeerMetric{
			{Name: "alice", HandshakeAge: 30, Online: 1, RxBytes: 10, TxBytes: 20},
		},
		S2SPeers: []S2SPeerMetric{
			{Name: "branch-a", HandshakeAge: -1, Online: 0},
		},
		PPPoEConnected: 1,
		IPv6Active:     1,
		IPv6Mode:       "6in4",
		FirewallActive: 1,
	}
	var buf bytes.Buffer
	if err := snap.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()

	required := []string{
		`# HELP lankeeper_build_info Build version and commit reported by the running binary.`,
		`# TYPE lankeeper_build_info gauge`,
		`lankeeper_build_info{commit="deadbeef",version="0.5.0-test"} 1`,
		`# TYPE lankeeper_uptime_seconds gauge`,
		`lankeeper_uptime_seconds 12345`,
		`lankeeper_cpu_percent 17.5`,
		`lankeeper_memory_total_bytes 8.589934592e+09`,
		`# TYPE lankeeper_interface_rx_bytes_total counter`,
		`lankeeper_interface_rx_bytes_total{device="eth0"} 100`,
		`lankeeper_interface_tx_bytes_total{device="wlan0"} 0`,
		`lankeeper_dhcp_active_leases 12`,
		`lankeeper_dns_queries_total 1000`,
		`lankeeper_client_rx_bytes_total{hostname="laptop",mac="abcdef01"} 50`,
		`lankeeper_client_tx_bps{hostname="laptop",mac="abcdef01"} 2`,
		`lankeeper_wireguard_peer_online{peer="alice"} 1`,
		`lankeeper_wireguard_peer_handshake_age_seconds{peer="alice"} 30`,
		`lankeeper_s2s_peer_online{peer="branch-a"} 0`,
		`lankeeper_s2s_peer_handshake_age_seconds{peer="branch-a"} -1`,
		`lankeeper_pppoe_connected 1`,
		`lankeeper_ipv6_mode_info{mode="6in4"} 1`,
		`lankeeper_firewall_active 1`,
	}
	for _, want := range required {
		if !strings.Contains(out, want) {
			t.Errorf("output missing line %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestExpositionEmptyCollectorsOmitFamilies(t *testing.T) {
	snap := MetricsSnapshot{IPv6Mode: "off"}
	var buf bytes.Buffer
	if err := snap.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()

	// Per-iface, per-client, per-peer families should be elided
	// when the snapshot has no rows. This bounds output size when
	// the router is freshly booted.
	for _, banned := range []string{
		`lankeeper_interface_rx_bytes_total`,
		`lankeeper_client_rx_bytes_total`,
		`lankeeper_wireguard_peer_online`,
		`lankeeper_s2s_peer_online`,
	} {
		if strings.Contains(out, banned) {
			t.Errorf("output unexpectedly contains %q", banned)
		}
	}
	// Singleton gauges still appear.
	if !strings.Contains(out, `lankeeper_uptime_seconds`) {
		t.Error("uptime gauge missing from empty snapshot")
	}
}

func TestEscapeLabelValue(t *testing.T) {
	cases := map[string]string{
		"plain":             "plain",
		`with"quote`:        `with\"quote`,
		`with\backslash`:    `with\\backslash`,
		"line\nbreak":       `line\nbreak`,
		"\ttab\rstripped":   "tabstripped",
		string([]byte{0x01, 'x', 0x1f, 'y'}): "xy",
	}
	for in, want := range cases {
		if got := escapeLabelValue(in); got != want {
			t.Errorf("escapeLabelValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMacHashStableAndShort(t *testing.T) {
	a := macHash("aa:bb:cc:dd:ee:ff")
	b := macHash("AA:BB:CC:DD:EE:FF")
	c := macHash("aabbccddeeff")
	if a != b || a != c {
		t.Errorf("macHash should be case- and separator-insensitive: %s %s %s", a, b, c)
	}
	if len(a) != 8 {
		t.Errorf("macHash length = %d, want 8", len(a))
	}
}

func TestParseWGDumpSkipsHeaderRow(t *testing.T) {
	// First line is the iface row (4 fields); peer row has 8.
	dump := strings.Join([]string{
		"privKEY\tpubKEY\t51820\toff",
		"peerKEY\tpsk\t1.2.3.4:1234\t10.10.11.2/32\t1700000000\t111\t222\t25",
	}, "\n")
	rows := parseWGDump(dump)
	if len(rows) != 1 {
		t.Fatalf("expected 1 peer row, got %d", len(rows))
	}
	row := rows["peerKEY"]
	if row.lastHandshake != 1700000000 || row.rxBytes != 111 || row.txBytes != 222 {
		t.Errorf("row parsed wrong: %+v", row)
	}
}
