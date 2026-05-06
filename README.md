# LANKeeper

A complete DIY home router, gateway, and NAS management software built in Go. Designed to replace ISP-provided modems with full control over networking, security, and media services. Ships as a single static binary with an embedded web interface, available for both `linux/amd64` and `linux/arm64`.

## Motivation

ISP modems (particularly Turkish ISPs like Turkcell Superonline) lack SQM/QoS support, causing severe bufferbloat on high-speed connections. This project provides CAKE-based traffic shaping, nftables firewall with atomic rollback, dual VPN engines, policy-based routing, and Samba NAS — all managed through a responsive web dashboard.

## Features

| Category       | Details                                                                   |
|----------------|---------------------------------------------------------------------------|
| WAN            | PPPoE with auto-reconnect, USB tethering failover, dual-stack IPv6       |
| Firewall       | nftables (dual-stack), NAT, port forwarding, custom rules, atomic apply  |
| DNS            | Unbound recursive resolver, DNS-over-TLS upstream, split-DNS overrides   |
| DHCP           | dnsmasq (DHCP-only), static leases mirrored to DNS records               |
| QoS            | CAKE qdisc (egress + IFB ingress), fq_codel, BBR/CUBIC congestion control |
| WireGuard      | Client tunnels, server with peer management, QR code provisioning        |
| OpenVPN        | easy-rsa PKI, client cert management, site-to-site with CCD/iroute       |
| Policy Routing | Source/destination/domain/port/schedule rules, fwmark + ip rule          |
| NAS            | Samba shares, M3U parser with group filtering, Kodi .strm generation     |
| Storage        | RAID-1 (mdadm), SMART monitoring, disk discovery                         |
| IPv6           | Dual-stack, DHCPv6-PD, SLAAC, ULA, ICMPv6 RFC 4890                       |
| VLANs          | Create/delete with isolation, per-VLAN DHCP                              |
| NTP            | chrony server with bind address, port, allow-subnet management           |
| Syslog         | rsyslog server (UDP/TCP/TLS RFC 5425) and forwarding client              |
| Backup         | Encrypted (AES-256-GCM) configuration export/import                      |
| Updates        | Over-the-air via GitHub Releases, atomic swap, 60s watchdog rollback     |
| Monitoring     | Real-time CPU/RAM/bandwidth via SSE, Canvas charts                       |
| System         | Hostname/domain/timezone management, TLS, factory reset                  |
| Deployment     | Offline installer ISO with preseed (amd64 + arm64), single-binary install |

## Hardware Requirements

Any x86_64 or ARM64 system with at least two Ethernet ports. Reference build:

- CPU: Intel i5 3470 (or equivalent), or any ARMv8 SBC
- RAM: 4 GB minimum, 8 GB recommended
- NIC: Onboard + PCIe gigabit (e.g., BCM5751)
- Storage: Single OS disk (NAS disks configured via web UI)
- PSU: PicoPSU or standard ATX

## Quick Start

The fastest path is the offline installer ISO from the latest release.

### Option 1: Pre-built Installer ISO

1. Download the architecture-matching ISO from the latest release at
   `https://github.com/KilimcininKorOglu/lankeeper/releases`:
   - `lankeeper-vX.Y.Z-installer-amd64.iso`
   - `lankeeper-vX.Y.Z-installer-arm64.iso`
2. Write to a USB stick: `dd if=lankeeper-vX.Y.Z-installer-amd64.iso of=/dev/sdX bs=4M status=progress`
3. Boot the target machine from the USB.
4. Answer six prompts shown by the Debian installer:
   locale (default `en_US.UTF-8`), keyboard (default Turkish Q),
   timezone (default `Europe/Istanbul`), hostname (default `hermes`),
   web-admin password, root password, then disk selection.
5. Install runs fully offline; the ISO embeds every required `.deb`.
6. After reboot, access the web UI at `https://<router-ip>:8443`.

### Option 2: Pre-built Binary on Existing Debian 12

```bash
# On the target router (Debian 12 Bookworm minimal)
curl -L -o lankeeper.tar.gz \
    https://github.com/KilimcininKorOglu/lankeeper/releases/latest/download/lankeeper-vX.Y.Z-linux-amd64.tar.gz
tar xzf lankeeper.tar.gz
sudo bash deploy/install.sh ./lankeeper
```

### Option 3: Build from Source

```bash
git clone https://github.com/KilimcininKorOglu/lankeeper.git
cd lankeeper
make build                       # local dev binary -> dist/lankeeper
make install                     # cross-compile + install on this host
```

### Building Installer ISOs

```bash
# Both architectures (requires Docker)
make iso-all \
    DEBIAN_AMD64_ISO=source_iso/debian-12-amd64-netinst.iso \
    DEBIAN_ARM64_ISO=source_iso/debian-12-arm64-netinst.iso

# Single architecture
make iso-amd64 DEBIAN_AMD64_ISO=...
make iso-arm64 DEBIAN_ARM64_ISO=...

# Full release pipeline (binaries + tarballs + ISOs + SHA256SUMS)
make release-all
```

Generated artifacts are written to `dist/`:

- `lankeeper-vX.Y.Z-linux-{amd64,arm64}` — static binaries
- `lankeeper-vX.Y.Z-linux-{amd64,arm64}.tar.gz` — release tarballs
- `lankeeper-vX.Y.Z-installer-{amd64,arm64}.iso` — offline installer ISOs
- `dist/packages/{amd64,arm64}/` — cached `.deb` package pools
- `SHA256SUMS` — SHA-256 of every published artifact

## Architecture

```
                    +---------------------------+
                    |    Web Browser (HTMX)     |
                    +-------------+-------------+
                                  | HTTPS :8443
                    +-------------v-------------+
                    |     lankeeper serve       |
                    |    (unprivileged user)    |
                    |                           |
                    |  Web Server + Auth + SSE  |
                    |  18 Services + Handlers   |
                    |  Template Renderer + i18n |
                    +-------------+-------------+
                                  | JSON-RPC 2.0
                                  | Unix Domain Socket
                    +-------------v-------------+
                    |     lankeeper agent       |
                    |          (root)           |
                    |                           |
                    |  Op Whitelist Dispatcher  |
                    |  nftables, ip, wg-quick   |
                    |  systemctl, pppd, mdadm   |
                    +---------------------------+
```

Two-process privilege separation: the web process never runs as root. All privileged operations go through the agent via a strict 44-command whitelist over a Unix domain socket. File operations are similarly path-restricted.

### Technology Stack

| Layer        | Technology                                             |
|--------------|--------------------------------------------------------|
| Language     | Go 1.22+ (standard library + 4 dependencies)           |
| Frontend     | HTMX + SSE + minimal vanilla JS                        |
| Templating   | Go `html/template` with layout inheritance             |
| Config       | YAML with AES-256-GCM encrypted credentials            |
| Firewall     | nftables with atomic apply + 30 s watchdog rollback    |
| DNS          | Unbound (recursive) + dnsmasq (DHCP only, port=0)      |
| VPN          | WireGuard + OpenVPN (easy-rsa PKI)                     |
| NAS          | Samba + M3U parser                                     |
| QoS          | CAKE qdisc + IFB ingress shaping                       |
| TLS          | Self-signed ECDSA P-256 (auto-generated), mkcert, ACME |
| Updates      | GitHub Releases + SHA-256 verify + atomic swap         |
| Deploy       | Single binary (`go:embed`), systemd, preseed ISO       |
| Dependencies | 4 direct Go modules (see table below)                  |

### Go Module Dependencies

| Module                        | Version | Purpose                                                   |
|-------------------------------|---------|-----------------------------------------------------------|
| `gopkg.in/yaml.v3`            | v3.0.1  | YAML config parsing and serialization (`router.yaml`)     |
| `golang.org/x/crypto`         | v0.50.0 | bcrypt password hashing, scrypt key derivation for backup |
| `github.com/gorilla/sessions` | v1.4.0  | Secure cookie-based HTTP session management               |
| `golang.org/x/net`            | v0.53.0 | DNS wire format (dnsmessage) for DoT upstream probe       |

No frontend build tools, no npm, no ORM, no database driver. The web frontend uses embedded HTMX with vanilla JavaScript.

## Web Interface

Dark-mode dominant design inspired by X (Twitter). All visible strings localized in Turkish and English with cookie-based language selection.

Pages: Dashboard, Network, Firewall, VPN (WireGuard), OpenVPN, Routing, DNS, DHCP, QoS, NAS, Storage, NTP, Syslog, Settings, Login.

## Configuration

Main config file: `/etc/lankeeper/router.yaml`

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

This project uses the Makefile for every build, test, and lint operation. Do not invoke `go build` or `go test` directly.

```bash
make dev                  # Quick dev build (no version ldflags)
make build                # Production build with version/commit/date
make test                 # go test ./... -race -count=1
make lint                 # golangci-lint run
make cross-all            # Cross-compile both architectures
make release-all          # Full release pipeline
make clean                # Remove build artifacts (preserves dist/packages/)
```

To run a single test group, pass the package and `-run` filter via `make test`'s underlying flags or run the test command after `make`-driven validation.

### Project Structure

```
cmd/lankeeper/          CLI entry: serve, agent, version, hash-password,
                         gen-cert, render-configs, help
internal/
  agent/                JSON-RPC 2.0 IPC (server + client, command and
                         path whitelists)
  config/               YAML structs, atomic writes, AES-256-GCM, TLS
  i18n/                 Flat dot-separated locale system
  netutil/              Exec wrapper, validators, AtomicChange
  services/             Business logic (18 services, one per domain)
  tmpl/                 Template renderer with layout inheritance
  web/                  HTTP server, auth, middleware, SSE broker
    handlers/           One handler per page (93 HTTP routes total)
web/
  templates/            HTML templates (layouts, pages, partials)
  static/               CSS, JS, icons (HTMX bundled)
  locales/              tr.json, en.json (must stay in sync)
configs/
  sysconf/              13 system config templates (nftables, unbound,
                         dnsmasq, chrony, smb, openvpn, wireguard,
                         pppoe, rsyslog)
  defaults/             Default YAML configs
deploy/                 install.sh, systemd units, preseed.cfg, ISO builder
```

## Deployment

### Systemd Services

```
lankeeper.target
  |- lankeeper-agent.service   (root, UDS listener at /run/lankeeper/agent.sock)
  |- lankeeper-web.service     (unprivileged lankeeper user, HTTPS :8443)
```

Install paths: binary at `/usr/local/bin/lankeeper`, config at `/etc/lankeeper/`, data at `/var/lib/lankeeper/`, logs at `/var/log/lankeeper/`.

### System Dependencies

The offline installer ISO ships every required `.deb`. When installing on an existing Debian 12 system via `deploy/install.sh`, the script installs the Debian "Standard system utilities" task plus the LANKeeper-specific packages below.

| Package              | Purpose                                              |
|----------------------|------------------------------------------------------|
| `nftables`           | Stateful firewall, NAT, port forwarding              |
| `iproute2`           | Network interface, VLAN, routing, tc/QoS control     |
| `ppp`                | PPPoE WAN connection (ISP fiber/DSL dial-up)         |
| `pppoe`              | PPPoE discovery and session daemon                   |
| `wireguard-tools`    | WireGuard VPN tunnel management                      |
| `openvpn`            | OpenVPN server/client tunnels                        |
| `easy-rsa`           | PKI certificate management for OpenVPN               |
| `unbound`            | Recursive DNS resolver with DNSSEC and blocklists    |
| `dnsmasq`            | DHCP server (DNS disabled, port=0)                   |
| `samba`              | SMB/CIFS NAS file sharing                            |
| `smartmontools`      | Disk health monitoring via S.M.A.R.T.                |
| `mdadm`              | Software RAID-1 array management                     |
| `chrony`             | NTP time synchronization (server + client)           |
| `rsyslog`            | Centralized syslog server and client                 |
| `dbus`               | System message bus (hostnamectl, timedatectl)        |
| `curl`, `jq`         | OTA update HTTP client + GitHub Releases JSON parser |
| `qrencode`           | QR code generation for WireGuard mobile configs      |
| `wide-dhcpv6-client` | DHCPv6 prefix delegation for IPv6 WAN                |
| `hdparm`             | Disk power management and standby control            |
| `htop`               | Operator-friendly process monitor                    |

The offline ISO additionally bundles the full Debian Standard task
(`less`, `nano`, `cron`, `logrotate`, `manpages`, `ca-certificates`,
`bind9-dnsutils`, `iputils-ping`, `traceroute`, `lsof`, `wget`, …) so
the target system has full operator ergonomics on first boot.

## Releases

Tagged releases are published at
`https://github.com/KilimcininKorOglu/lankeeper/releases`. Each release
includes the four artifact files plus `SHA256SUMS` for verification.

The `lankeeper update` subcommand (and the Settings -> Update page in
the web UI) consume this feed automatically: a `runtime.GOARCH`-matched
`.tar.gz` is fetched, SHA-256 verified, swapped atomically, and rolled
back by a 60 s watchdog if the new binary fails its health check. The
GRUB boot menu is rebranded with the new version on success.

## License

This project is private software. All rights reserved.
