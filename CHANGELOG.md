# Changelog

All notable changes to LANKeeper are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **(qos) Per-client bandwidth visibility**: `/qos` now ships a live
  table that lists every DHCP-known client with its hostname, MAC,
  current download/upload throughput, cumulative byte counters and a
  60-sample sparkline trend. Backed by a dedicated `lankeeper_qos`
  nftables table whose forward chain hooks at priority -200 with one
  `ether saddr/daddr` counter pair per MAC; `nft -j list table inet
  lankeeper_qos` is sampled every 2 seconds and broadcast on a new
  `/events/qos` SSE channel separate from the dashboard stream. The
  sampler resyncs the counter set from the lease file every minute,
  caps tracked clients at 64 to bound rule growth, and ships zero new
  JS dependencies (extends the existing in-tree canvas helper).
- **(ipv6) Drag-and-drop SLA-ID reassignment**: the `/ipv6` Announced
  card now lets the operator reorder VLANs to control which sub-/64
  each one receives from the delegated prefix. The primary LAN is
  pinned at SLA-ID 0; remaining rows take 1, 2, 3 ... in submission
  order. Backed by `IPv6Service.SetSubnetMap` (validates "lan"=0,
  rejects unknown VLAN IDs / negatives / duplicates) and
  `POST /ipv6/subnet-map`. The order persists to
  `cfg.IPv6.LAN.SubnetMap` and triggers a dhcp6c.conf rewrite plus a
  dnsmasq RA drop-in reload so clients see the new prefix on the
  next RA cycle.

### Changed

- **(web) Generic data-sortable helper**: the in-tree
  `htmx-sortable.js` now reads the reorder URL from a
  `data-reorder-url` attribute and supports a `data-pin-first` flag
  for tables where the first row must stay put. The previous hard
  wiring to `/routing/reorder` (never actually attached to a
  template) is gone.

## [0.4.0] - 2026-05-06

First-class 6in4 tunneling for operators whose ISP refuses to
deliver native IPv6. The /ipv6 dashboard now exposes a Plane
selector that flips the entire IPv6 stack between DHCPv6-PD (the
existing path, unchanged) and a Hurricane Electric 6in4 tunnel
terminated locally on the router.

### Added

- **(ipv6) 6in4 tunneling end-to-end**: new SixInFourService owns
  the sit interface lifecycle (`ip tunnel add mode sit ...` →
  `link set up mtu` → `addr add ClientIPv6` → `-6 route add ::/0`)
  with PPPoE-aware MTU (1452 over PPPoE, 1480 direct). State is
  persisted to `/var/lib/lankeeper/state/ipv6-tunnel.json` for
  observability. Restart is idempotent: any prior tunnel is torn
  down before the new one is brought up.
- **(ipv6) HE.net /nic/update DDNS client**: posts the new IPv4
  endpoint with HTTP Basic Auth on every PPPoE reconnect (when
  AutoUpdate is enabled). Identical-IPv4 calls dedupe to zero HTTP
  hits to stay under HE's "abuse" rate limiter; only good/nochg
  responses are cached. End-to-end coverage via httptest.
- **(firewall) IPv6 tunnel ingress and forward**: the input chain
  now accepts protocol-41 packets from `cfg.IPv6.Tunnel.ServerIPv4`
  via the new `IPv6WANInterfaces` slice, and the forward chain
  permits LAN ↔ tunnel device traffic. NAT66 is intentionally
  disabled — IPv6 is end-to-end routed by design.
- **(ipv6) RA renderer is mode-aware**: in 6in4 mode the RA
  drop-in derives /64 sub-prefixes from `Tunnel.RoutedPrefix`
  (typically /48) and advertises the tunnel MTU (1452/1480) so
  clients clamp MSS for the encapsulated path. PD mode behaviour
  is unchanged.
- **(ipv6) Tunnel form, status card, and manual DDNS**: the /ipv6
  page exposes the full HE.net tunnel definition (Server IPv4,
  Client IPv6, Routed prefix, Tunnel ID, Username, Update Key) and
  a live status card with device, MTU, local IPv4, last
  /nic/update reply, and RX/TX byte counters. UpdateKey persists
  through empty submits (the form shows a "leave blank to keep"
  placeholder). New POST /ipv6/tunnel/update route triggers a
  manual DDNS push without waiting for the next PPPoE reconnect.
- **(config) IPv6TunnelConfig schema**: new struct fields
  `Mode` ("dhcpv6-pd" / "6in4"), `Tunnel.{ServerIPv4, ClientIPv6,
  RoutedPrefix, TunnelID, Username, UpdateKey, AutoUpdate, Device}`.
  UpdateKey is stored plaintext like PPPoE.Password — the agent
  socket is the trust boundary. Default device is `lkt6in4` to
  avoid collisions with stock he-ipv6 systemd units.
- **(test) Cross-service integration test for 6in4**: drives the
  PPPoE on-connect callback by hand against real services and the
  fakeAgent harness, asserting the full v0.4.0 contract — DDNS
  POST → tunnel add chain → RA drop-in rewrite → dnsmasq reload —
  plus dedup behaviour on identical-IPv4 reconnects.

### Changed

- **(ipv6) ApplyConfig now stops dhcp6c whenever Mode == "6in4"**:
  the two planes are mutually exclusive at the daemon level, so
  the IPv6 service tears down the PD client when the operator
  switches to 6in4 (and vice versa via the handler's mode-swap
  guard). RenderToDisk writes a distinct stub into `dhcp6c.conf`
  so an operator inspecting the file sees that PD is intentionally
  idle.
- **(ipv6) PPPoE on-connect / on-disconnect hooks are mode-aware**:
  in PD mode the existing dhcp6c restart/stop chain runs; in 6in4
  mode the hook pushes the new IPv4 to HE.net (when AutoUpdate is
  on), rebuilds the sit interface, and re-applies the RA drop-in.

## [0.3.1] - 2026-05-06

Lifecycle hardening for the IPv6 lease watcher and the first
cross-service integration test in the repo.

### Fixed

- **(ipv6) StopLeaseWatcher now cancels the pending 150ms debounce
  timer**: previously the timer was a local var inside
  runLeaseWatcher, so a `time.AfterFunc` callback could still fire
  after Stop returned. The stale dispatch then ran against
  torn-down state (cleared agent client, replaced config) and
  produced spurious "permission denied" log lines plus a data race
  surfaced by the new integration test. Timer is now a struct
  field stopped under mu.Lock before close(stopCh).

### Changed

- **(test) Cross-service integration test for the lease-driven
  firewall apply chain**: `ipv6_firewall_integration_test.go`
  wires real `IPv6Service` + real `FirewallService` together the
  same way `web/server.go` does in production. A fake agent
  records every `exec.run` / `file.write` / `file.read` call so
  the production code path runs unchanged. Asserts the dnsmasq RA
  drop-in is rewritten, dnsmasq is reload-or-restarted, and the
  firewall Apply chain runs (snapshot/validate/apply nft, with
  `-c` only on validate). Same harness will cover 6in4 lease
  watching in v0.4.0.

## [0.3.0] - 2026-05-06

IPv6 reaches feature parity. wide-dhcpv6 already delegates an ISP
prefix in v0.2.0; v0.3.0 makes it actually useful end-to-end —
dnsmasq announces every /64 sub-prefix per LAN/VLAN, RDNSS/DNSSL
get pumped through the Router Advertisement (RFC 8106), the firewall
re-applies on every lease change, ULA bootstraps itself, and the
operator finally sees prefix lifetime / RDNSS state on `/ipv6`.

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

- **IPv6 RA pumps RDNSS / DNSSL through to clients**: the dnsmasq
  drop-in now embeds `option6:dns-server` per LAN/VLAN derived from the
  RDNSS field of the dhcp6c lease state, plus an `option6:domain-search`
  for `cfg.System.Domain`. RA is re-rendered automatically on every
  lease event so clients learn DNS the IPv6-native way (RFC 8106)
  instead of relying on DHCPv4 option 6.
- **RA advertises link MTU**: `ra-param` now includes `mtu:1492` when
  PPPoE is the WAN, `mtu:1500` otherwise, so clients negotiate the
  correct MSS over IPv6.
- **ULA auto-bootstrap**: when `cfg.IPv6.LAN.ULA.Enabled = true` and
  `Prefix = ""`, IPv6Service generates a `fdXX:XXXX:XXXX::/48` from a
  40-bit cryptographically random Global ID per RFC 4193 and persists
  it via `cfg.SaveToFile()`. Subsequent renders reuse the same prefix.
- **Prefix expiry awareness**: `PrefixState` gains `Expired()` and
  `ExpiresIn()` helpers driven by the lease timestamp + valid lifetime.
  `Active()` now returns false once the lifetime has elapsed even
  before dhcp6c writes a RELEASE. `/ipv6` status card surfaces a
  countdown badge plus an "Expired" badge when applicable.
- `/ipv6` page now also surfaces the operator's RDNSS list, search
  domain and current ULA prefix (or a "will be generated" placeholder).
- Locale keys added to tr.json and en.json: `ipv6.expired`,
  `ipv6.expiresIn`, `ipv6.expiresInTitle`, `ipv6.rdnssHelp`,
  `ipv6.searchDomain`, `ipv6.ulaPrefix`, `ipv6.ulaPending`.

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
