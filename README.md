# Home Router

A complete DIY home router, gateway, and NAS management software built in Go. Designed to replace ISP-provided modems with full control over networking, security, and media services. Ships as a single static binary with an embedded web interface.

## Motivation

ISP modems (particularly Turkish ISPs like Turkcell Superonline) lack SQM/QoS support, causing severe bufferbloat on high-speed connections. This project provides CAKE-based traffic shaping, nftables firewall with atomic rollback, dual VPN engines, policy-based routing, and Samba NAS -- all managed through a responsive web dashboard.

## Features

| Category       | Details                                                                   |
|----------------|---------------------------------------------------------------------------|
| WAN            | PPPoE with auto-reconnect, USB tethering failover                         |
| Firewall       | nftables (dual-stack), NAT, port forwarding, custom rules, TTL fix        |
| DNS            | Unbound recursive resolver, blocklist, query logging                      |
| DHCP           | dnsmasq (DHCP-only), static leases, auto DNS registration                 |
| QoS            | CAKE qdisc (egress + IFB ingress), fq_codel, BBR/CUBIC congestion control |
| WireGuard      | Client tunnels, server with peer management, site-to-site VPN             |
| OpenVPN        | easy-rsa PKI, client cert management, site-to-site with CCD/iroute        |
| Policy Routing | Source/destination/domain/port/schedule rules, fwmark + ip rule           |
| NAS            | Samba shares, M3U parser with group filtering, Kodi .strm generation      |
| Storage        | RAID management (mdadm), SMART monitoring, disk discovery                 |
| IPv6           | Dual-stack, DHCPv6-PD, SLAAC, ULA, ICMPv6 RFC 4890                        |
| VLANs          | Create/delete with isolation, per-VLAN DHCP                               |
| Monitoring     | Real-time CPU/RAM/bandwidth via SSE, Canvas charts                        |
| System         | Hostname/domain/timezone management, TLS, backup/restore, factory reset   |
| Deployment     | Offline ISO installer with preseed, single-binary install                 |

## Hardware Requirements

Any x86_64 system with at least two Ethernet ports. Reference build:

- CPU: Intel i5 3470 (or equivalent)
- RAM: 4 GB minimum, 8 GB recommended
- NIC: Onboard + PCIe gigabit (e.g., BCM5751)
- Storage: Single OS disk (NAS disks configured via web UI)
- PSU: PicoPSU or standard ATX

## Quick Start

### Prerequisites

- Go 1.22+ (build machine)
- Debian 12 Bookworm (target machine)

### Build

```bash
git clone https://github.com/KilimcininKorOglu/home-router.git
cd home-router
make build
```

### Install on Target

```bash
# Cross-compile and install
make cross
scp home-router root@<router-ip>:/tmp/
ssh root@<router-ip> bash /tmp/deploy/install.sh /tmp/home-router
```

### ISO Installer (Offline)

```bash
make iso DEBIAN_ISO=/path/to/debian-12-netinst.iso
# Burns a preseed ISO with embedded packages -- no internet required during install
```

The preseed installer asks 6 questions: hostname, root password, web UI password, timezone, keyboard layout, and disk selection.

## Architecture

```
                    +---------------------------+
                    |     Web Browser (HTMX)    |
                    +-------------+-------------+
                                  | HTTPS :8443
                    +-------------v-------------+
                    |    home-router serve       |
                    |    (unprivileged user)     |
                    |                           |
                    |  Web Server + Auth + SSE   |
                    |  16 Services + Handlers    |
                    |  Template Renderer + i18n  |
                    +-------------+-------------+
                                  | JSON-RPC 2.0
                                  | Unix Domain Socket
                    +-------------v-------------+
                    |    home-router agent       |
                    |    (root)                  |
                    |                           |
                    |  Op Whitelist Dispatcher   |
                    |  nftables, ip, wg-quick    |
                    |  systemctl, pppd, mdadm    |
                    +---------------------------+
```

Two-process privilege separation: the web process never runs as root. All privileged operations go through the agent via a strict operation whitelist over a Unix domain socket.

### Technology Stack

| Layer        | Technology                                             |
|--------------|--------------------------------------------------------|
| Language     | Go 1.22+ (standard library + 3 dependencies)           |
| Frontend     | HTMX + SSE + minimal vanilla JS                        |
| Templating   | Go `html/template` with layout inheritance             |
| Config       | YAML with AES-256-GCM encrypted credentials            |
| Firewall     | nftables with atomic apply + 30s watchdog rollback     |
| DNS          | Unbound (recursive) + dnsmasq (DHCP only, port=0)      |
| VPN          | WireGuard + OpenVPN (easy-rsa PKI)                     |
| NAS          | Samba + M3U parser                                     |
| QoS          | CAKE qdisc + IFB ingress shaping                       |
| TLS          | Self-signed ECDSA P-256 (auto-generated), mkcert, ACME |
| Deploy       | Single binary (`go:embed`), systemd, preseed ISO       |
| Dependencies | 3 direct Go modules (see table below)                  |

### Go Module Dependencies

| Module                        | Version | Purpose                                                   |
|-------------------------------|---------|-----------------------------------------------------------|
| `gopkg.in/yaml.v3`            | v3.0.1  | YAML config parsing and serialization (`router.yaml`)     |
| `golang.org/x/crypto`         | v0.50.0 | bcrypt password hashing, scrypt key derivation for backup |
| `github.com/gorilla/sessions` | v1.4.0  | Secure cookie-based HTTP session management               |

No frontend build tools, no npm, no ORM, no database driver. The web frontend uses embedded HTMX with vanilla JavaScript.

## Web Interface

Dark-mode dominant design inspired by X (Twitter). All text localized (Turkish and English) with cookie-based language selection.

Pages: Dashboard, Network, Firewall, VPN (WireGuard), OpenVPN, Routing, DNS, DHCP, QoS, NAS, Storage, Syslog, NTP, Settings.

## Configuration

Main config file: `/etc/home-router/router.yaml`

All config structs are defined in `internal/config/config.go`. The file is written atomically (tmp -> fsync -> rename) and credentials are encrypted with AES-256-GCM.

### Default Networks

| Segment    | Subnet        | Notes               |
|------------|---------------|---------------------|
| LAN        | 10.10.10.0/24 | Primary LAN         |
| WireGuard  | 10.10.11.0/24 | VPN tunnel pool     |
| OpenVPN    | 10.8.0.0/24   | Configurable        |
| Guest VLAN | 10.10.13.0/24 | Isolated by default |

Default hostname: `hermes`, domain: `lan` (FQDN: `hermes.lan`).

## Development

```bash
make dev                  # Build without ldflags
make test                 # Run all tests with race detector
make lint                 # golangci-lint
go test ./internal/services/ -run TestFirewall -v   # Specific test group
go build ./...            # Type-check
go vet ./...              # Static analysis
```

### Project Structure

```
cmd/home-router/        CLI entry point (serve, agent, version, hash-password, gen-cert)
internal/
  agent/                JSON-RPC 2.0 IPC (server + client)
  config/               YAML config structs, crypto, TLS
  i18n/                 Localization engine
  netutil/              Exec wrapper, validators, AtomicChange
  services/             Business logic (one file per domain)
  tmpl/                 Template renderer
  web/                  HTTP server, auth, middleware, SSE
    handlers/           One handler per page
web/
  templates/            HTML templates (layouts, pages, partials)
  static/               CSS, JS, icons
  locales/              tr.json, en.json
configs/
  sysconf/              System config templates (nftables, unbound, etc.)
  defaults/             Default YAML configs
deploy/                 install.sh, systemd units, preseed ISO builder
```

## Deployment

### Systemd Services

```
home-router.target
  |- home-router-agent.service   (root, UDS listener)
  |- home-router-web.service     (unprivileged, HTTPS)
```

### System Dependencies (installed by install.sh)

| Package              | Version (Bookworm) | Purpose                                              |
|----------------------|--------------------|------------------------------------------------------|
| `nftables`           | 1.0.6              | Stateful firewall, NAT, port forwarding              |
| `iproute2`           | 6.1.0              | Network interface, VLAN, routing, and tc/QoS control |
| `ppp`                | 2.4.9              | PPPoE WAN connection (ISP fiber/DSL dial-up)         |
| `pppoe`              | 3.15               | PPPoE discovery and session daemon                   |
| `wireguard-tools`    | 1.0.20210914       | WireGuard VPN tunnel management                      |
| `openvpn`            | 2.6.3              | OpenVPN server/client tunnels                        |
| `easy-rsa`           | 3.1.0              | PKI certificate management for OpenVPN               |
| `unbound`            | 1.17.1             | Recursive DNS resolver with DNSSEC and blocklists    |
| `dnsmasq`            | 2.90               | DHCP server (DNS disabled, port=0)                   |
| `samba`              | 4.17.12            | SMB/CIFS NAS file sharing for LAN clients            |
| `smartmontools`      | 7.3                | Disk health monitoring via S.M.A.R.T.                |
| `mdadm`              | 4.2                | Software RAID-1 array management                     |
| `chrony`             | 4.3                | NTP time synchronization (server + client)           |
| `rsyslog`            | 8.2302.0           | Centralized syslog server and client                 |
| `curl`               | 7.88.1             | HTTP client for OTA update checks                    |
| `jq`                 | 1.6                | JSON parsing for GitHub Releases API                 |
| `qrencode`           | 4.1.1              | QR code generation for WireGuard mobile configs      |
| `wide-dhcpv6-client` | 20080615           | DHCPv6 prefix delegation for IPv6 WAN                |
| `hdparm`             | 9.65               | Disk power management and standby control            |

## License

This project is private software. All rights reserved.
