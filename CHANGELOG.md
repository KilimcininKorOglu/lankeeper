# Changelog

All notable changes to LANKeeper are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **dnsmasq IPv6 RA per VLAN**: a new dnsmasq drop-in
  (`/etc/dnsmasq.d/lankeeper-ipv6-ra.conf`) is rewritten by the IPv6
  service on every apply. wide-dhcpv6 already assigns a /64 sub-prefix
  to the LAN bridge and each VLAN; dnsmasq now advertises that sub-prefix
  to clients via SLAAC so they finally auto-configure global IPv6
  addresses. The drop-in honours `cfg.IPv6.LAN.RAInterval` and the ULA
  prefix when enabled.
- `IPv6Service.RenderRAConfig` / wired into `RenderToDisk`+`ApplyConfig`
  so install-time and runtime apply both produce the drop-in.
- `dnsmasq.conf.tmpl` gains `conf-dir=/etc/dnsmasq.d,*.conf` so the
  primary config picks up the drop-in without a separate include.
- The dhcp6c lease-event hook now issues
  `systemctl reload-or-restart dnsmasq` so RA picks up freshly
  delegated /64 sub-prefixes immediately after every lease change
  (best-effort: missing dnsmasq is non-fatal).
- `/ipv6` page gains an "Announced Sub-Prefixes" table listing each
  LAN/VLAN device that receives a /64 sub-prefix and its sla-id.
  Backed by `IPv6Service.AnnouncedInterfaces()`. Locale keys
  `ipv6.announced`, `ipv6.announcedHelp`, `ipv6.interface`, `ipv6.slaId`
  added to both tr.json and en.json.
- **Lease-driven firewall refresh**: `IPv6Service` now watches
  `/var/lib/lankeeper/state/ipv6-prefix.json` via fsnotify and fires a
  registered callback whenever the dhcp6c hook script swaps the file
  in. Wired in `web/server.go` so the firewall ruleset re-applies
  (with auto-confirm) after every prefix change. Identical leases
  are deduped via a Prefix/Reason/Lifetime hash so renewals do not
  cause spurious reloads.

### Dependencies

- Added `github.com/fsnotify/fsnotify` for IPv6 lease state file watching.

## [0.2.0] - 2026-05-06

DHCPv6 Prefix Delegation: LANKeeper now requests an IPv6 prefix
from the ISP (RFC 8415), persists the lease, and carves a /64 sub-prefix
out of the delegation for every downstream interface (LAN bridge plus
each VLAN). A dedicated `/ipv6` page exposes lease status and full
lifecycle controls.

### Added

- **DHCPv6-PD client**: `wide-dhcpv6` (`dhcp6c`) integrated as
  `lankeeper-dhcp6c.service`, conflicts with Debian's stock unit so
  the two never race. Lease events persisted by a hook script to
  `/var/lib/lankeeper/state/ipv6-prefix.json`.
- **IPv6Service**: 3-layer config rendering (RenderConfig /
  RenderToDisk / ApplyConfig) plus Start/Stop/Restart/Renew/Release
  lifecycle. WAN device resolution honours PPPoE (uses `ppp0` when
  `cfg.PPPoE.Username` is set). Prefix hint validated to /48..\/64;
  sla-len auto-derived as `64 - delegated_length`.
- **VLAN sub-prefix assignment**: `IPv6LANConfig.SubnetMap` (operator
  override keyed by VLAN ID) plus auto-incrementing `sla-id` per VLAN.
  /64 delegations correctly skip VLAN entries (no subnet bits left).
- **/ipv6 web page**: status card with delegated prefix, last event
  reason, preferred/valid lifetimes, RDNSS; action buttons for
  Renew / Release / Start / Stop; WAN config form bound to mode,
  prefix hint, request prefix, rapid commit. Sidebar entry between
  DHCP and QoS. Full Turkish/English locale parity.
- **PPPoE cross-service hooks**: `SetOnConnect` / `SetOnDisconnect`
  registrations let the IPv6 service restart `dhcp6c` whenever
  `ppp0` is rebuilt.

### Changed

- **Agent whitelist** (44 -> 46 commands): `dhcp6c`, `dhcp6ctl` added.
  Path whitelist gains `/etc/wide-dhcpv6/` and `/var/lib/lankeeper/`
  for read+write.
- **render-configs subcommand**: `ipv6/dhcp6c` step renders both the
  daemon config and the lease hook script at install time so the
  service boots correctly on first start.
- **Config schema**: `IPv6WANConfig.RapidCommit` (default true) for
  the two-message DHCPv6 exchange.

## [0.1.0] - 2026-05-06

Initial public release. LANKeeper is a single-binary Go + HTMX home
router/gateway/NAS targeting Debian 12, with two-process privilege
separation (unprivileged web UI + root agent over JSON-RPC on a
Unix domain socket).

### Added

- **Networking core**: PPPoE WAN, dual-stack IPv4/IPv6, VLAN support,
  static and dynamic routing, USB tethering fallback, multi-NIC
  bridging via first-boot wizard.
- **Firewall**: nftables with atomic apply and 30 s watchdog
  rollback, rendered from a versioned template, with bootstrap
  ruleset loaded before `sshd` starts.
- **DNS**: Unbound recursive resolver with optional DNS-over-TLS
  upstream, inline DoT connectivity probe, split-DNS overrides,
  static A/AAAA/PTR records, per-record reverse PTR opt-out.
- **DHCP**: dnsmasq DHCP server with static leases that auto-mirror
  to persistent DNS records (`Source: dhcp-static`); domain change
  rebuilds all mirrored records.
- **VPN**: WireGuard server + clients with QR provisioning, OpenVPN
  server + clients via easy-rsa PKI.
- **NAS**: Samba shares with M3U playlist parser, SMART monitoring,
  RAID-1 via mdadm, storage device management.
- **QoS**: CAKE qdisc with IFB ingress shaping, per-interface
  bandwidth control.
- **NTP**: chrony server with bind address, port, and allow-subnet
  management.
- **Syslog**: rsyslog server (UDP/TCP/TLS RFC 5425) and forwarding
  client with facility routing and TLS UI.
- **Backup**: encrypted configuration export/import (AES-256-GCM),
  tar archive ingest with path-traversal protection.
- **OTA updates**: GitHub Releases consumer with `runtime.GOARCH`
  asset selection, SHA-256 verification, atomic binary swap, 60 s
  watchdog rollback, GRUB version branding, persistent state
  surviving restarts.
- **Web UI**: HTMX + SSE, dark mode, full Turkish/English i18n
  (every visible string), session auth (bcrypt + gorilla/sessions),
  CSRF double-submit cookie, LAN-only IP whitelist, per-IP rate
  limiter, automatic ECDSA P-256 TLS certificate generation, mkcert
  and ACME support, Content-Security-Policy header.
- **Deployment**: offline preseed installer ISO (amd64 + arm64),
  Docker-based ISO builder with cached `.deb` repository, install
  script, systemd target orchestrating root agent + unprivileged
  web service, install-time config rendering for unbound, dnsmasq,
  chrony, rsyslog, smbd.

### Security

- Two-process privilege separation: web service runs as `lankeeper`
  user, all system commands route through a root agent over a
  localhost Unix domain socket (mode 0666).
- Strict agent command whitelist (44 binaries) and typed file path
  rules (dir prefix, exact file, filename prefix) with symlink
  resolution.
- Bootstrap nftables ruleset shipped before SSH start to prevent
  WAN exposure during the boot transient.
- Firewall, DNS, NTP, syslog input validators reject newline
  injection in rendered config files.
- ACME and self-signed TLS certificates generated server-side; no
  default password and no random fallback (admin sets the password
  during install).

### Known Limitations

- Single admin user; no role-based access control.
- IPv6 prefix delegation handled by `wide-dhcpv6-client` only; no
  DHCPv6-PD UI yet.
- 6in4/IPv6 tunneling not implemented.
