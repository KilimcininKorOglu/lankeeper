# LANKeeper

A complete DIY home router, gateway, and NAS management software built in Go. Designed to replace ISP-provided modems with full control over networking, security, and media services. Ships as a single static binary with an embedded web interface, available for both `linux/amd64` and `linux/arm64`.

## Motivation

ISP modems (particularly Turkish ISPs like Turkcell Superonline) lack SQM/QoS support, causing severe bufferbloat on high-speed connections. This project provides CAKE-based traffic shaping, nftables firewall with atomic rollback, dual VPN engines, policy-based routing, and Samba NAS — all managed through a responsive web dashboard.

## Features

| Category       | Details                                                                          |
|----------------|----------------------------------------------------------------------------------|
| WAN            | PPPoE with auto-reconnect, USB tethering failover, dual-stack IPv6               |
| Firewall       | nftables (dual-stack), NAT, port forwarding, per-port rate limits, atomic apply  |
| DNS            | Unbound recursive resolver, DNS-over-TLS or DNS-over-HTTPS upstream, split-DNS   |
| DHCP           | dnsmasq (DHCP-only), static leases mirrored to DNS records                       |
| QoS            | CAKE qdisc (egress + IFB ingress), per-MAC nftables byte counters                |
| WireGuard      | Client tunnels, server with peer management, QR provisioning, site-to-site wizard |
| OpenVPN        | easy-rsa PKI, client cert management, site-to-site with CCD/iroute               |
| Policy Routing | Source/destination/domain/port/schedule rules, fwmark + ip rule                  |
| NAS            | Samba shares, M3U parser with group filtering, Kodi .strm generation             |
| Storage        | RAID-1 (mdadm), SMART monitoring, disk discovery                                 |
| IPv6           | Dual-stack, DHCPv6-PD, SLAAC, ULA, ICMPv6 RFC 4890, HE.net 6in4 tunnel           |
| VLANs          | Create/delete with isolation, per-VLAN DHCP                                     |
| NTP            | chrony server with bind address, port, allow-subnet management                  |
| Syslog         | rsyslog server (UDP/TCP/TLS RFC 5425) and forwarding client                     |
| Backup         | Encrypted (AES-256-GCM) archives to local disk, S3-compatible storage, or SFTP, on a cron schedule |
| Monitoring     | Real-time CPU/RAM/bandwidth via SSE with Canvas charts, plus a Prometheus `/metrics` endpoint |
| Updates        | Over-the-air via GitHub Releases, atomic swap, 60 s watchdog rollback           |
| TLS            | Three modes from the UI: self-signed, mkcert local CA, ACME over DNS-01         |
| System         | Hostname/domain/timezone management, factory reset, first-boot bridge          |
| Deployment     | Offline installer ISO with preseed (amd64 + arm64), single-binary install       |

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
4. Answer the installer prompts: locale (default `en_US.UTF-8`), keyboard
   (default Turkish Q), timezone (default `Europe/Istanbul`), hostname
   (default `hermes`), web-admin password, whether root may log in over
   SSH with a password (default no), root password, and disk selection.
5. Install runs fully offline; the ISO embeds every required `.deb`.
6. After reboot, access the web UI at `https://<router-ip>:8443`.

On the first boot only, every physical NIC is enslaved into a `br0`
bridge at `10.10.10.1/24`, so the UI answers on whichever port you happen
to have plugged in before any WAN interface has been assigned. Completing
setup clears the first-boot flag but deliberately leaves the bridge up,
because it holds the address you are connected on; tearing it down is a
separate, explicit action.

Root SSH defaults to key-only access. Answer yes to the SSH question only
if you need password login as root; a missing or unrecognised answer is
never read as consent.

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
make build                       # production binary -> dist/lankeeper
make install                     # build for this host's architecture + install
```

`deploy/install.sh` refuses a binary that does not match the target
architecture before it changes anything.

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

The source Debian image is checksum-verified against
`deploy/iso/debian-images.sha512` before any of `xorriso`, `fdisk`, or
`dd` touches it.

Generated artifacts are written to `dist/`:

- `lankeeper-linux-{amd64,arm64}` — static binaries, no version in the name
- `lankeeper-vX.Y.Z-linux-{amd64,arm64}.tar.gz` — release tarballs
- `lankeeper-vX.Y.Z-installer-{amd64,arm64}.iso` — offline installer ISOs
- `release-{amd64,arm64}/lankeeper` — tarball staging copies
- `dist/packages/{amd64,arm64}/` — cached `.deb` package pools
- `SHA256SUMS` — SHA-256 of the published tarballs and ISOs

`make dev` and `make build` write `dist/lankeeper` for the host platform.
Only the tarballs and ISOs carry the version in their filename.

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
                    |  Services + 133 Routes    |
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

Two-process privilege separation: the web process never runs as root. All privileged operations go through the agent via a strict 47-command whitelist over a Unix domain socket. File operations are similarly path-restricted, with separate rule sets for reads and writes, so a readable path is not automatically writable.

The whitelist checks more than the name. The agent discards the caller's path string and re-resolves the basename against a fixed set of trusted bin directories, so a file merely named after an allowed command cannot be executed. The environment is decided agent-side too: the RPC carries no env field, because the loader honours `LD_PRELOAD` at execve time whatever binary was validated.

The socket is the privilege boundary itself. It is owned `root:lankeeper` at mode 0660 (the group is the agent's `--service-group` flag, defaulting to `lankeeper`), falls back to owner-only 0600 when the group cannot be resolved, and the agent additionally verifies the peer UID through `SO_PEERCRED`. The agent serves at most 16 connections concurrently, which also bounds how many privileged subprocesses can run at once.

### Technology Stack

| Layer        | Technology                                                                        |
|--------------|-----------------------------------------------------------------------------------|
| Language     | Go 1.26.5 (standard library + 6 dependencies)                                     |
| Frontend     | HTMX + SSE + minimal vanilla JS                                                   |
| Templating   | Go `html/template` with layout inheritance                                        |
| Config       | YAML with AES-256-GCM encrypted credentials                                       |
| Firewall     | nftables with atomic apply + 30 s watchdog rollback                               |
| DNS          | Unbound (recursive) + dnsmasq (DHCP only, port=0); dnscrypt-proxy stub for DoH upstream |
| VPN          | WireGuard (road-warrior + site-to-site) + OpenVPN (easy-rsa PKI)                   |
| NAS          | Samba + M3U parser                                                                |
| QoS          | CAKE qdisc + IFB ingress shaping; per-MAC nftables counters                       |
| Backup       | Local + S3-compatible (native SigV4, no AWS SDK) + SFTP, cron-scheduled           |
| Monitoring   | Prometheus `/metrics`, exposition format 0.0.4, written with the standard library |
| TLS          | Self-signed ECDSA P-256 (auto-generated), mkcert local CA, ACME DNS-01 (Let's Encrypt staging by default) |
| Updates      | GitHub Releases + SHA-256 verify + atomic swap                                    |
| Deploy       | Single binary (`go:embed`), systemd, preseed ISO                                  |

### Go Module Dependencies

| Module                          | Version  | Purpose                                                   |
|---------------------------------|----------|-----------------------------------------------------------|
| `gopkg.in/yaml.v3`              | v3.0.1   | YAML config parsing and serialization (`router.yaml`)     |
| `golang.org/x/crypto`           | v0.54.0  | bcrypt password hashing, scrypt key derivation for backup |
| `golang.org/x/net`              | v0.56.0  | DNS wire format (dnsmessage) for the DoT/DoH probe        |
| `github.com/gorilla/sessions`   | v1.4.0   | Secure cookie-based HTTP session management               |
| `github.com/pkg/sftp`           | v1.13.10 | SFTP backup target uploads                                |
| `github.com/fsnotify/fsnotify`  | v1.10.1  | DHCPv6 lease file watcher                                 |

All six are imported by production code and sit in the direct require
block. `kr/fs`, `gorilla/securecookie`, and `golang.org/x/sys` are
genuinely transitive. `dnscrypt-proxy` is a Debian system package, not a
Go dependency.

No frontend build tools, no npm, no ORM, no database driver. The web frontend uses embedded HTMX with vanilla JavaScript.

## Web Interface

Dark-mode dominant design inspired by X (Twitter). All visible strings localized in Turkish and English with cookie-based language selection.

Pages: Dashboard, Network, Firewall, VPN (WireGuard), VPN site-to-site, OpenVPN, Routing, DNS, DHCP, IPv6, QoS, NAS, Storage, NTP, Syslog, Backup, Settings, Login.

## Monitoring

LANKeeper exposes a Prometheus-compatible scrape endpoint at `GET /metrics` with the standard text exposition format (`text/plain; version=0.0.4`). The endpoint is LAN-only by virtue of the same middleware that gates the admin UI: callers from outside the configured LAN subnets receive `403 Forbidden`. No authentication is required because Prometheus scrapers do not carry session cookies; the trust boundary is the network perimeter.

Metric families exposed (all prefixed `lankeeper_`):

| Family                                  | Type    | Labels         |
|-----------------------------------------|---------|----------------|
| `build_info`                            | gauge   | version,commit |
| `uptime_seconds`                        | gauge   | -              |
| `cpu_percent`                           | gauge   | -              |
| `memory_total_bytes` / `memory_used_bytes` | gauge   | -              |
| `temperature_celsius`                   | gauge   | -              |
| `interface_rx_bytes_total` / `_tx_`     | counter | device         |
| `dhcp_active_leases`                    | gauge   | -              |
| `dns_queries_total` / `cache_hits_total` / `cache_misses_total` / `blocked_total` | counter | - |
| `client_rx_bytes_total` / `_tx_` / `_rx_bps` / `_tx_bps` | counter+gauge | mac (hashed) |
| `wireguard_peer_online` / `_handshake_age_seconds` / `_rx_bytes_total` / `_tx_` | gauge+counter | peer (hashed) |
| `s2s_peer_online` / `_handshake_age_seconds` | gauge | peer (hashed) |
| `openvpn_active_sessions`               | gauge   | -              |
| `backup_last_run_timestamp` / `_last_status_ok` / `_history_total` | gauge | - |
| `pppoe_connected` / `ipv6_active` / `firewall_active` | gauge | - |
| `ipv6_mode_info`                        | gauge   | mode           |

The endpoint carries no authentication, so it names nobody. Every operator- or client-assigned identifier is reduced to a stable 8-character SHA-1 prefix before it is emitted: `mac` for LAN clients and `peer` for WireGuard and site-to-site peers, the latter because a remote peer is not otherwise observable from the local segment, which made the exposition itself the disclosure. The hash is a series key, not a security control; it keeps each subject a distinct, stable series without labelling it. There is no `hostname` label at all, since `mac` already keys the series and a second identifier would add cardinality and no information.

Per-client rows are capped at 64 per scrape to bound cardinality on busy networks. The scrape is a pure read: it composes cached service state and read-only probes, and a dead subsystem degrades its own family rather than failing the whole endpoint.

Sample Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: lankeeper
    scheme: https
    metrics_path: /metrics
    tls_config:
      insecure_skip_verify: true   # router uses self-signed ECDSA P-256
    static_configs:
      - targets: ['10.10.10.1:8443']
```

## Configuration

Main config file: `/etc/lankeeper/router.yaml`

All config structs are defined in `internal/config/config.go`. The file is written atomically (tmp -> fsync -> rename). Backup target credentials are encrypted at rest with AES-256-GCM behind an `enc:v1:` prefix, keyed from `/var/lib/lankeeper/credentials/config.key`.

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
make cross                # Cross-compile linux/amd64
make cross-all            # Cross-compile both architectures
make install              # Build for this host's architecture, then install
make check                # Verify install prerequisites on the target
make iso / make iso-all   # Offline preseed installer ISOs (requires Docker)
make release-all          # Full release pipeline
make clean                # Remove build artifacts (preserves dist/packages/)
```

Running a subset of the tests is the one place a raw `go test` is
expected, since the Makefile has no per-test target. Keep `-race` and
`-count=1`; test caching is not allowed.

```bash
go test ./internal/services/ -run TestVPN -race -count=1 -v   # single test or prefix
go test ./internal/web/handlers/ -race -count=1               # single package
```

### Continuous Integration

`.github/workflows/ci.yml` runs seven gates across five jobs on push and
pull request to `main`: `go build ./...`,
`go test ./... -race -count=1` and `go vet ./...` (all three in the
`build-test` job), then `golangci-lint`, `govulncheck ./...`,
`gosec ./...`, and `make cross-all`, which also runs the amd64 artifact
and checks that its stamped version is not the un-stamped fallback.
`make lint test cross-all` covers most of it locally.

A non-zero `gosec` exit means something genuinely new. Every standing
finding carries a line-scoped `// #nosec` with its justification, so the
baseline is exit 0.

Third-party actions are pinned to a full commit SHA and tool versions are
explicit, so two runs of the same tree execute the same code. Tests in
`buildsys/` enforce that rather than leaving it to convention. There is
no Dependabot configuration, so every module bump, action SHA and tool
version is raised by hand.

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
  services/             Business logic (38 non-test files, one service
                         per domain)
  tmpl/                 Template renderer with layout inheritance
  web/                  HTTP server, auth, middleware, SSE broker
    handlers/           One handler per page (23 files, 133 HTTP routes)
web/
  templates/            HTML templates (layouts, pages, partials)
  static/               CSS, JS, icons (htmx 2.0.10 vendored, not a CDN)
  locales/              tr.json, en.json (must stay in sync)
configs/
  sysconf/              17 system config templates (nftables, unbound,
                         dnsmasq, chrony, smb, openvpn, wireguard,
                         pppoe, rsyslog, dnscrypt-proxy)
  defaults/             Default YAML configs
deploy/                 install.sh, systemd units, preseed.cfg, ISO builder
buildsys/               Test-only. Guards release checksums, artifact
                         names, CI workflow pins, gofmt config, ISO bind
                         mounts, the go.mod require blocks, the CSP
                         template rules, and the vendored asset digests
```

`buildsys/` and `deploy/iso/` hold no production code, so they cannot
break `go build ./...` or `go vet ./...`. They exist to guard properties
a unit test on the Go source cannot reach, such as the shape of a build
recipe, the contents of an installer shell script, or the SHA-256 of a
vendored browser bundle. That last one is not hypothetical: the htmx file
was once a 212-byte placeholder, which left every `hx-post` and `hx-get`
in the tree inert with nothing reporting it.

## Deployment

### Systemd Services

```
lankeeper.target
  |- lankeeper-agent.service   (root, UDS listener at /run/lankeeper/agent.sock)
  |- lankeeper-web.service     (unprivileged lankeeper user, HTTPS :8443)
```

Install paths: binary at `/usr/local/bin/lankeeper`, config at `/etc/lankeeper/`, data at `/var/lib/lankeeper/`, logs at `/var/log/lankeeper/`.

### System Dependencies

`deploy/install.sh` installs the packages below on an existing Debian 12
system.

| Package                       | Purpose                                              |
|-------------------------------|------------------------------------------------------|
| `nftables`                    | Stateful firewall, NAT, port forwarding              |
| `iproute2`                    | Network interface, VLAN, routing, tc/QoS control     |
| `ppp`, `pppoe`                | PPPoE WAN discovery and session daemon               |
| `wireguard-tools`             | WireGuard VPN tunnel management                      |
| `openvpn`, `easy-rsa`         | OpenVPN tunnels and PKI certificate management       |
| `unbound`                     | Recursive DNS resolver with DNSSEC and blocklists    |
| `dnsmasq`                     | DHCP server (DNS disabled, port=0)                   |
| `dnscrypt-proxy`              | DoH upstream stub on 127.0.0.1:5353                  |
| `samba`, `samba-common-bin`   | SMB/CIFS NAS file sharing                            |
| `smartmontools`               | Disk health monitoring via S.M.A.R.T.                |
| `mdadm`                       | Software RAID-1 array management                     |
| `hdparm`                      | Disk power management and standby control            |
| `chrony`                      | NTP time synchronization (server + client)           |
| `rsyslog`                     | Centralized syslog server and client                 |
| `mkcert`                      | Local CA for the mkcert TLS mode                     |
| `wide-dhcpv6-client`          | DHCPv6 prefix delegation for IPv6 WAN                |
| `curl`, `jq`                  | OTA update HTTP client + GitHub Releases JSON parser |

The offline installer ISO ships every required `.deb` and additionally
bundles the full Debian Standard task (`less`, `nano`, `cron`,
`logrotate`, `manpages`, `ca-certificates`, `bind9-dnsutils`,
`iputils-ping`, `traceroute`, `lsof`, `wget`, …) plus `dbus`,
`openssh-server`, and `htop`, so the target system has full operator
ergonomics on first boot. Installing from the ISO and installing onto an
existing Debian host therefore do not produce an identical package set.

## Releases

Tagged releases are published at
`https://github.com/KilimcininKorOglu/lankeeper/releases`. Each release
includes two tarballs and two installer ISOs, plus `SHA256SUMS` for
verification.

The Settings -> Update page in the web UI consumes this feed: a
`runtime.GOARCH`-matched `.tar.gz` is fetched, SHA-256 verified, swapped
atomically, and rolled back by a 60 s watchdog if the new binary fails
its health check. The GRUB boot menu is rebranded with the new version on
success. There is no `update` CLI subcommand; updates are driven from the
web UI.

Read `CHANGELOG.md` before applying an update. Every fix and security
change is listed there, which is how an operator decides whether a
release is worth taking. That file was restarted from a bare header, so
it carries no section for v0.5.0 or earlier; those release notes live on
their GitHub Releases and the full detail is in the git history.

## License

Released under the [MIT License](LICENSE). Copyright (c) 2026 KilimcininKorOglu.
