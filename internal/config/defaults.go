package config

func DefaultConfig() *Config {
	return &Config{
		System: SystemConfig{
			Hostname:  "hermes",
			Domain:    "lan",
			Timezone:  "Europe/Istanbul",
			Language:  "tr",
			WebPort:   8443,
			WebBind:   "10.10.10.1",
			TLS: TLSConfig{
				Mode: "self-signed",
				SelfSigned: SelfSignedConfig{
					CN:        "hermes.lan",
					ValidDays: 3650,
					SANs:      []string{"hermes.lan", "10.10.10.1", "router.local"},
				},
			},
		},
		PPPoE: PPPoEConfig{
			MTU:             1492,
			MRU:             1492,
			LCPEchoInterval: 10,
			LCPEchoFailure:  3,
			Persist:         true,
			Holdoff:         5,
			IPv6CP:          true,
		},
		Firewall: FirewallConfig{
			DefaultPolicy: "drop",
			TTLFix:        TTLFixConfig{Value: 64},
			RateLimits:    map[string]string{"ssh": "3/minute", "web": "30/minute"},
		},
		QoS: QoSConfig{
			Profile:           "cake",
			UploadKbps:        40000,
			DownloadKbps:      950000,
			CongestionControl: "bbr",
		},
		DHCP: DHCPConfig{
			RangeStart: "10.10.10.100",
			RangeEnd:   "10.10.10.250",
			LeaseTime:  "12h",
			Gateway:    "10.10.10.1",
			DNSServer:  "10.10.10.1",
		},
		IPv6: IPv6Config{
			Enabled: "auto",
			Mode:    "dhcpv6-pd",
			WAN:     IPv6WANConfig{AcceptRA: true, RequestPrefix: true, PrefixHint: "/56"},
			LAN:     IPv6LANConfig{Mode: "slaac", ULA: IPv6ULAConfig{Enabled: true, Prefix: "fd00:abcd:1234::/48"}, RAInterval: 30, RDNSS: true},
			Privacy: true,
		},
		DNS: DNSConfig{
			BlocklistURLs:           []string{"https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"},
			BlocklistUpdateSchedule: "0 3 * * *",
			CacheSize:               50000,
			QueryLog:                QueryLogConfig{Enabled: true, LogPath: "/var/log/unbound/queries.log", MaxSize: "100M", Retention: "7d", LogBlocked: true},
		},
	}
}
