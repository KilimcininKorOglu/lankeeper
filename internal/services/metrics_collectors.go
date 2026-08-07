package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// collectClientMetrics turns the QoS sampler output into a flat
// slice of ClientBandwidthMetric. We cap the number of rows at
// 64 to bound the per-scrape cardinality on busy LANs - the same
// limit the QoS dashboard already enforces.
func (s *MetricsService) collectClientMetrics() []ClientBandwidthMetric {
	if s.qos == nil {
		return nil
	}
	usages, err := s.qos.SamplePerClient(context.Background())
	if err != nil {
		return nil
	}
	if len(usages) == 0 {
		return nil
	}
	const maxClients = 64
	if len(usages) > maxClients {
		usages = usages[:maxClients]
	}
	out := make([]ClientBandwidthMetric, 0, len(usages))
	for _, u := range usages {
		out = append(out, ClientBandwidthMetric{
			MAC:     macHash(u.MAC),
			RxBytes: u.InBytes,
			TxBytes: u.OutBytes,
			RxBPS:   u.InBPS,
			TxBPS:   u.OutBPS,
		})
	}
	return out
}

// collectVPNMetrics returns (road-warrior peers, site-to-site peers).
// Road-warrior peers come from `wg show wg0 dump`; S2S peers reuse
// VPNService.S2SHealth so the parsing logic stays single-sourced.
func (s *MetricsService) collectVPNMetrics(ctx context.Context) ([]WGPeerMetric, []S2SPeerMetric) {
	if s.cfg == nil {
		return nil, nil
	}
	wg := s.collectRoadWarriorPeers(ctx)
	s2s := s.collectS2SPeers(ctx)
	return wg, s2s
}

// collectRoadWarriorPeers parses `wg show wg0 dump` and joins each
// row against cfg.VPN.Server.Peers. We skip site-to-site peers
// (handled separately) and pending peers that haven't completed
// their join handshake.
func (s *MetricsService) collectRoadWarriorPeers(ctx context.Context) []WGPeerMetric {
	if !s.cfg.VPN.Server.Enabled || len(s.cfg.VPN.Server.Peers) == 0 {
		return nil
	}
	out, err := netutil.RunSimple(ctx, "wg", "show", "wg0", "dump")
	if err != nil {
		return nil
	}
	dump := parseWGDump(out)
	peers := make([]WGPeerMetric, 0, len(s.cfg.VPN.Server.Peers))
	now := time.Now().Unix()
	for _, p := range s.cfg.VPN.Server.Peers {
		if p.IsSiteToSite || p.Pending {
			continue
		}
		row, ok := dump[p.PublicKey]
		m := WGPeerMetric{
			Name:         peerHash(p.Name),
			HandshakeAge: -1,
		}
		if ok {
			if row.lastHandshake > 0 {
				m.HandshakeAge = now - row.lastHandshake
				if m.HandshakeAge < 180 {
					m.Online = 1
				}
			}
			m.RxBytes = row.rxBytes
			m.TxBytes = row.txBytes
		}
		peers = append(peers, m)
	}
	return peers
}

// collectS2SPeers reuses VPNService.S2SHealth so the wg dump
// parser stays in one place. The vpn package is already
// authoritative for site-to-site peer state.
func (s *MetricsService) collectS2SPeers(ctx context.Context) []S2SPeerMetric {
	if s.vpn == nil || len(s.cfg.VPN.Server.Peers) == 0 {
		return nil
	}
	out := make([]S2SPeerMetric, 0)
	for _, p := range s.cfg.VPN.Server.Peers {
		if !p.IsSiteToSite || p.Pending {
			continue
		}
		health, err := s.vpn.S2SHealth(ctx, p.Name)
		if err != nil || health == nil {
			out = append(out, S2SPeerMetric{
				Name:         peerHash(p.Name),
				HandshakeAge: -1,
			})
			continue
		}
		online := 0
		if health.Online {
			online = 1
		}
		out = append(out, S2SPeerMetric{
			Name:         peerHash(health.Name),
			HandshakeAge: health.HandshakeAgeSec,
			Online:       online,
			RxBytes:      health.RxBytes,
			TxBytes:      health.TxBytes,
		})
	}
	return out
}

// wgDumpRow captures the per-peer columns from `wg show <iface> dump`.
// Format (tab-separated, 8 fields):
//
//	pubkey  psk  endpoint  allowed_ips  latest_handshake  rx_bytes  tx_bytes  keepalive
type wgDumpRow struct {
	pubKey        string
	lastHandshake int64
	rxBytes       uint64
	txBytes       uint64
}

// parseWGDump turns the multi-line output of `wg show ... dump`
// into a pubkey-keyed map. The first line is the interface row and
// is dropped; peer rows have exactly 8 tab-separated fields.
func parseWGDump(out string) map[string]wgDumpRow {
	rows := make(map[string]wgDumpRow)
	for i, line := range strings.Split(out, "\n") {
		if i == 0 {
			// Interface row: privkey pubkey listenport fwmark
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 8 {
			continue
		}
		row := wgDumpRow{pubKey: fields[0]}
		_, _ = fmt.Sscanf(fields[4], "%d", &row.lastHandshake)
		_, _ = fmt.Sscanf(fields[5], "%d", &row.rxBytes)
		_, _ = fmt.Sscanf(fields[6], "%d", &row.txBytes)
		rows[row.pubKey] = row
	}
	return rows
}
