# Changelog

All notable changes to LANKeeper are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Security

- **(nas) Share path can no longer inject Samba directives**: the share
  path was checked only with `filepath.Clean` and a `/srv/` or `/mnt/`
  prefix test. Neither rejects an embedded newline, which an ordinary
  form POST can carry as `%0A`, and `Clean` preserves control characters
  outright. The value was rendered through `text/template`, which
  performs no escaping, into the unquoted `path =` line of the share
  stanza, so a newline ended that directive and began another inside the
  same block. `smbd` runs as root and Samba implements `root preexec`
  and similar directives that execute commands, so an injected rule ran
  as root when a client connected - and because the same form sets
  `guest ok`, the trigger was an unauthenticated LAN SMB connection.
  Paths are now checked against a character allowlist on the raw value,
  before normalisation. Share name, path, and valid-users entries are
  re-validated at render time as well, so an entry from hand-edited
  YAML or a restored backup cannot reach `smb.conf` either; an invalid
  share is skipped and logged, leaving the rest of the configuration
  intact.

- **(agent) Agent socket no longer grants root to every local account**:
  the privileged agent chmod'ed its Unix socket to `0666` and performed
  no authentication on connect - no peer-credential check, no token, no
  UID comparison. Any local process, under any user, could invoke
  `exec.run` against the root agent. The command whitelist constrains
  command names but not arguments and includes `chpasswd`, `usermod`,
  `cp`, `chmod`, `rm`, and `mount`, so a single call was enough to take
  the machine. This nullified the two-process privilege separation the
  product is built around. The socket is now owned by `root` and the
  service group with mode `0660`, and on Linux the agent additionally
  verifies the peer's UID through `SO_PEERCRED`, accepting only root and
  the service account. If the service group cannot be resolved the
  socket stays owner-only and the agent logs why, so a misconfigured
  install fails closed instead of falling back to a permissive mode.
  The socket path and the service user/group are settable with
  `-socket`, `-service-user`, and `-service-group`.

### Fixed

- **(backup) Archives now include the OpenVPN PKI**: `Export` built its
  archive from the config directory plus `/etc/unbound` and
  `/etc/dnsmasq.d` only. The easy-rsa PKI under `/etc/openvpn` was left
  out, and none of that material is mirrored into `router.yaml`, which
  stores only names, ports, ciphers, and per-client metadata. Restoring
  such an archive left the OpenVPN server unable to start, and
  regenerating the CA invalidated every previously issued client
  certificate, so every road-warrior and site-to-site peer had to be
  re-provisioned by hand. WireGuard was unaffected because its private
  keys do live in `router.yaml`. A directory that does not exist is now
  skipped and logged rather than failing the whole archive, so an
  appliance with no OpenVPN configured still backs up.

- **(dns) Default Unbound cache size no longer exceeds the hardware**:
  the shipped default was `cacheSize: 50000`. The field carries no unit
  and the Unbound template appends `m` to it across three directives,
  with the rrset cache at twice the base, so a fresh install rendered a
  combined cache request of roughly 195 GiB against the documented 4 GB
  minimum. The service-side fallback only substituted a sane value when
  the setting was exactly `0`, so the shipped non-zero default bypassed
  it. Unbound cannot start with that allocation, DNS has no fallback
  path on this appliance, and the field is not exposed in the web UI, so
  recovery meant hand-editing YAML over SSH. The default is now 64 MB
  and the renderer clamps the value to 4-256 MB, logging when it does.
  The clamp also repairs existing installs that already have the old
  value persisted in `router.yaml`, which changing the default alone
  would not.

- **(firewall) Custom rules now reach the live ruleset**: the UI offered
  a full add/remove/toggle flow with an Enabled badge, and the generator
  that turns those rules into nftables text existed, but nothing ever
  called it - the only occurrence of `GenerateCustomNftRules` in the
  tree was its own definition. The template had no placeholder and the
  template data had no field, so a rule shown as Enabled changed
  nothing. This failed open: an operator adding a drop rule to block a
  device was told it was active while the packet filter was untouched.
  Rules are now compiled per chain and rendered. They are placed after
  the conntrack lines but ahead of the built-in accepts, so an explicit
  rule wins and a new drop rule actually blocks new connections;
  appending them at the end would have left drop rules inert for traffic
  the built-ins already accepted. The rule's `chain` field is honoured,
  which the original generator ignored. Rule name and interface are now
  validated both at intake and at render time, since the nftables file
  has no escaping and a rule from hand-edited YAML or a restored backup
  could otherwise inject arbitrary statements. Rules carrying no match
  condition are skipped rather than rendered as an unconditional accept
  or drop for the whole chain.

- **(backup) Factory Reset now works on every install**: the reset
  derived its source directory from the config directory's parent,
  which resolved to `/etc/configs/defaults` - a path no installer
  ever creates. `POST /system/factory-reset` therefore returned
  HTTP 500 on any router installed by the documented process, and
  the appliance was neither reset nor rebooted. The shipped default
  YAML files are now embedded in the binary, so the reset works on
  any install layout and always restores the defaults matching the
  running version, including after an OTA update that replaces only
  the binary. Restored files are written `0640` to match the mode
  the installer applies, instead of the previous world-readable
  `0644`; `router.yaml` carries the session secret and the admin
  password hash. Write failures are now reported instead of being
  logged and swallowed, which previously let the router reboot
  while reporting a successful reset.

## [0.5.0] - 2026-05-07

### Added

- **(monitoring) Prometheus `/metrics` endpoint**: LAN-only,
  exposition format 0.0.4, no auth (scrapers carry no session
  cookies; the LAN-only middleware is the trust boundary). About
  30 metric families covering build info, uptime, CPU/RAM/temp,
  per-interface byte counters, DHCP lease count, DNS query/hit/
  miss/blocked totals, per-client bandwidth (capped 64 rows,
  SHA1[:4] MAC label for stable cardinality), WireGuard road-
  warrior peers, S2S peers, OpenVPN session count, backup last-
  run/status/history, PPPoE/IPv6/firewall liveness gauges, and
  an IPv6 `mode_info` label-style metric. Stdlib-only writer
  (~50 LOC `fmt.Fprintf`) - no `client_golang` dependency.
- **(dns) DNS-over-HTTPS upstream**: a new `/dns` encryption-mode
  card lets the operator pick Plain (recursive) / DoT / DoH for
  upstream resolution. DoH support routes through a
  `dnscrypt-proxy` stub on `127.0.0.1:5353` (Unbound itself does
  not implement DoH upstream in any version, so a stub daemon is
  required); the proxy is installed by `apt install dnscrypt-proxy`
  but stays disabled until the operator selects DoH. Built-in
  resolver catalogue covers Cloudflare (3 filter tiers), Quad9
  (filter + nofilter), Google, AdGuard (filter + unfiltered),
  NextDNS placeholder and Mullvad. Custom upstreams accept an
  `https://host/dns-query` URL or a `sdns://` stamp; both go
  through SSRF guard (internal-IP rejection), a 443/4443/8443
  port whitelist and host/path char allowlist before persisting.
  `/dns/doh/probe` runs a one-shot RFC8484 query (rate-limited via
  the same per-client limiter as DoT). DoT and DoH are mutually
  exclusive at the form layer with a backend assertion as
  defence-in-depth. Apply order (dnscrypt-proxy first, then Unbound
  reload) prevents a `~10s` retry storm on toggle-on.
- **(backup) Automated snapshot scheduling**: a new `/backup` page
  drives encrypted config exports against multiple destinations on
  a cron schedule. Supported targets: local (`/var/lib/lankeeper/
  backups/`), S3-compatible (real S3 + MinIO + Backblaze B2 +
  DigitalOcean Spaces via native SigV4 signing — no aws-sdk-go
  dep), and SFTP (password or private-key auth via `pkg/sftp`).
  Cron dialect supports the `@hourly`/`@daily`/`@weekly`/
  `@monthly`/`@yearly` aliases plus 5-field `M H DOM Mo DOW`
  with `*`, single values, comma lists, ranges and `*/k` steps.
  Per-target retention prunes old backups (newest N kept) so a
  flaky remote doesn't drag down healthy ones. Run history is a
  50-entry ring buffer persisted to `router.yaml` so audit trails
  survive restarts. Atomic write everywhere: tmp + rename for
  local and SFTP, single PUT for S3. Encrypted with AES-256-GCM
  via the existing scrypt-derived passphrase pipeline; empty
  passphrase on form submit preserves the stored value (mirrors
  PPPoE.Password and HE.UpdateKey UX). New endpoints:
  `GET /backup`, `POST /backup/schedule`, `POST /backup/target`,
  `DELETE /backup/target/{name}`, `POST /backup/run`,
  `GET /backup/history`.
- **(vpn) Site-to-Site wizard**: a new `/vpn/s2s` page lets two
  LANKeeper routers establish a WireGuard mesh by exchanging two
  HMAC-signed tokens — no manual key copy/paste between
  `wg0.conf` files. The originator issues an invite token (peer
  name, public endpoint, expected remote LAN); the joining side
  consumes it, registers the originator as a peer and returns an
  ack token bearing its own public key. Pending peers are skipped
  in `wireguard-server.conf` until the ack arrives, garbage
  collected after invite expiry (60 min default), and applied
  live via `wg syncconf` so the running tunnel never bounces.
  Peer subnet conflicts with local LANs are rejected at issue
  time. New endpoints: `/vpn/s2s/invite`, `/vpn/s2s/join`,
  `/vpn/s2s/finalize`, `DELETE /vpn/s2s/{name}`,
  `/vpn/s2s/{name}/health`, `/vpn/s2s/{name}/reachability` (single
  ICMP echo over wgs0).
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
