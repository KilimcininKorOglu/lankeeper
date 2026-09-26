# Changelog

All notable changes to LANKeeper are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/).

Entries start again from this point. Release notes for v0.5.0 and
earlier live on their GitHub Releases, and the full detail is in the
git history.

## [Unreleased]

## [0.5.6] - 2026-09-26

A release-pipeline release. The router code is unchanged from 0.5.5;
what changes is how the published files are produced. Updating is
optional.

### Added

- Release archives and installer ISOs are now built on GitHub Actions
  from the pushed tag, for amd64 and on a native arm64 runner, instead
  of on the maintainer's machine. `SHA256SUMS` is signed in a job that
  runs only after the maintainer approves it and after CI passed on the
  tagged commit, so a commit that failed CI is never signed.
- Signing refuses a key whose signature does not verify against the
  public key compiled into the router. A wrong key now stops the release
  before anything is published, instead of producing a release every
  router would reject.
- `make iso-amd64` and `make iso-arm64` download the Debian source image
  when it is missing; the ISO builder still checks it against the pinned
  digests. `make release-notes` writes the CHANGELOG section for a
  version to `dist/RELEASE_NOTES.md` and refuses a version with no
  section.

## [0.5.5] - 2026-09-26

A correctness and hardening release. The largest changes close paths
from the unprivileged web process to root through the agent, make OTA
updates verify a maintainer signature, and fix a long list of features
that were configurable but did not work on a real router.

Read before updating:

- **Signed updates start here.** This release verifies an ed25519
  signature (`SHA256SUMS.sig`) over `SHA256SUMS` against a key compiled
  into the binary, and refuses any later release without one. Routers
  on v0.5.1 still install this release unsigned; from this release on,
  only signed releases install.
- **Reinstall for the full fix set.** OTA replaces only the binary.
  Changes to systemd units, installer scripts and `configs/sysconf`
  templates (the dhcp6c unit, the bootstrap firewall, the web unit's
  runtime directory, the Unbound, dnsmasq and OpenVPN templates) reach
  an existing router only through a reinstall.
- **Backup passphrases** must now be at least 12 characters. Existing
  archives made with a shorter passphrase still import.
- **`/etc/dnsmasq.d` is no longer archived.** Its only LANKeeper file is
  rendered from `router.yaml`; older archives' entries for it are skipped.

### Added

- Site-to-site WireGuard peers export rx/tx byte counters on `/metrics`.
- The configured blocklist schedule now runs; before, only the manual
  update ever refreshed the lists.
- `/api/version` answers with a content `ETag` so clients can revalidate.
- Every refused agent request and every executed privileged command is
  logged, so an intrusion attempt through the agent leaves a trace.

### Fixed

- **Firewall.** Custom rules and port mutators take the service lock,
  so a background apply no longer races an edit. A background apply can
  no longer re-install a ruleset the watchdog rolled back. The TTL
  rewrite moved to a filter chain (in a nat chain it touched only the
  first packet), the MSS clamp now precedes the accept rules that used to
  skip it, LAN-to-USB-tether forwarding works when USB NAT is on, and a
  PPPoE WAN is matched as `ppp0`. The confirmed ruleset persists to
  `/etc/nftables.conf`, so it survives a reboot.
- **VPN.** Site-to-site links route only the far LANs, the ack is
  authenticated with the invite's preshared key, and invites refuse
  public prefixes, `/0` and subnets overlapping existing peers, VLANs or
  the OpenVPN subnet. The WireGuard server key pair is generated when
  missing. The peer list is read and persisted only under the service
  lock, so the status page and `/metrics` can no longer see a torn list.
- **OpenVPN.** Fixed client addresses are pushed under `topology subnet`
  with the server mask and refused when the server cannot push them;
  `tls-auth` and compression framing appear in client profiles only when
  the server uses them; the client PKI is read through the agent.
- **DHCP and DNS.** VLAN clients get the router as gateway and resolver
  and a DHCP range with real addresses. A static lease outside every
  served subnet is refused instead of shown while dnsmasq ignores it.
  Unbound accepts queries from every client subnet the router serves,
  has remote control enabled (every `unbound-control` call used to
  fail), and is re-rendered after a static lease changes. The query log
  rotates and its tail starts with the server; only one blocklist update
  runs at a time.
- **IPv6.** The RA no longer advertises a zero router lifetime (which
  removed the default route), the ULA `dhcp-range` renders a valid start
  address, the router is the first RDNSS entry, and `accept_ra=2` is set
  on the WAN. The dhcp6c unit is installed and receives its interface.
  6in4 RX/TX counters show real values.
- **VLANs and network.** Every VLAN field is validated before it is
  stored, a subnet overlapping a served network is refused, concurrent
  adds and deletes can no longer lose a change, and VLAN devices and MAC
  clones are recreated at startup.
- **QoS.** Download is counted per client by destination IP, clearing
  shaping removes the WAN ingress qdisc (which otherwise dropped all
  inbound traffic), and the page shows its empty state.
- **Backup.** Imports are staged and validated before anything is
  written, S3 listings follow continuation tokens and refuse a bad
  timestamp, SigV4 query encoding is exact, retention touches only
  LANKeeper objects, local targets are writable by the service, a
  schedule change recomputes the next run, and the backup config is
  guarded by one lock.
- **TLS and ACME.** Issuance and TLS config writes are serialized, a
  renewal no longer overwrites a mode the operator just switched to, the
  served certificate reloads after renewal, and renewal failures and the
  pending manual DNS record appear in the UI.
- **Auth and web.** Sessions end on logout and password change and live
  no longer than the cookie. A password change requires the current
  password. Unauthenticated htmx and SSE requests send the browser to the
  login page instead of silently failing. Request bodies are bounded
  before the form is parsed, and long handlers and SSE streams lift the
  write deadline.
- **Other.** NTP, syslog, storage, PPPoE, health check and metrics
  parsers were corrected against real Debian 12 output: chrony 4
  sources, forwarded syslog facilities, `lsblk` JSON, SMART status,
  pppd liveness, and a metric family whose collector failed is omitted
  instead of exported as zero. M3U sources run on their own schedules.

### Security

- **Agent boundary.** `exec.run` arguments are validated per command
  (`cp`, `rm`, `chmod`, `tar`, `mount`, `mkdir`, `chpasswd`, `mkcert`,
  `systemctl`), so the service account can no longer copy over `/etc` or
  add itself to a group. File operations refuse `..`, act through a root
  opened at the rule's directory so a swapped symlink cannot redirect a
  root write, and carry binary content intact. The write whitelist names
  exact files in directories root scans for code (`grub.d`, `/etc/ppp`,
  `dnsmasq.d`, `wide-dhcpv6`), and `file.write` refuses execute bits
  except on the dhcp6c script. Scratch files no longer go to the host
  `/tmp`; nft scripts and the WireGuard sync config reach their command
  on stdin.
- **OTA.** Updates require a maintainer signature over `SHA256SUMS`, the
  archive name must carry the release tag so an old signed release
  cannot be replayed as a downgrade, releases are fetched through the
  guarded clients, and the rollback guard runs outside the process being
  replaced.
- **Secrets.** The root password reaches `chpasswd` on stdin instead of
  argv, the session secret and WireGuard preshared keys are encrypted at
  rest, the pre-update snapshot is owner-only from creation and lives
  outside the service account's directory, and the mkcert CA root sits
  in a root-owned directory.
- **Login.** A global failed-login budget sits beside the per-address
  guard: past 30 failures an hour across all addresses, password checks
  are spaced 30 s apart.
- **Network exposure.** Samba binds only to LAN addresses and runs only
  while a share exists, the bootstrap ruleset drops forwarded traffic,
  the 6in4 tunnel no longer admits new inbound connections to the LAN,
  DoT verifies the upstream certificate, and the outbound guard refuses
  the router's own IPv6 LAN prefixes.
- **Input into root-run configs.** Syslog remote hosts, manual peer
  endpoints, outbound OpenVPN configs, static lease hostnames and PPPoE
  credentials are validated before they reach a file a root daemon
  reads. DHCP clients can no longer publish over operator DNS names.
- **Dependencies.** Go floor 1.26.8, `x/crypto` past the SSH channel DoS
  advisories, and `github.com/pkg/sftp` v1.13.11, which fixes an
  attribute-count allocation a malicious SFTP server could use to kill
  the web process.

## [0.5.1] - 2026-08-08

First release since the changelog was restarted. It covers everything
merged after v0.5.0, so the list is long: three TLS modes, network
configuration from the UI, and a large correctness and hardening pass
across the agent boundary, the firewall, backup, and the web layer.

### Added

- Three TLS modes are now selectable from Settings. `self-signed`
  generates in-process, `mkcert` installs a local CA whose root the UI
  hands out for download, and `acme` issues and auto-renews a public
  certificate over DNS-01. ACME defaults to the Let's Encrypt staging
  directory, so a misconfigured first attempt cannot burn the
  production rate limit.
- WireGuard peer and OpenVPN client configurations render as a scannable
  QR code, so a phone can be provisioned without transferring a file.
- A WireGuard peer's configuration can be re-issued after its first
  download, which previously left the operator with no way to recover a
  lost config short of deleting and recreating the peer.
- Network interfaces are configurable from the UI instead of by editing
  the config file.
- USB tethering is now an operator control rather than an implicit
  failover path.
- On the first boot only, every physical NIC is enslaved into a `br0`
  bridge at `10.10.10.1/24`, so the UI answers on whichever port happens
  to be plugged in before any WAN interface has been assigned.
- Firewall rate limits apply per open port, replacing the old
  service-keyed map that could not express two limits on one service.
- The firewall TTL fix is exposed as a setting.

### Fixed

- **Firewall.** Custom rules and open ports are now rendered into the
  nftables ruleset; both were configurable but inert. An apply is
  refused while another change is still pending, and refused outright
  when no rollback snapshot could be taken, so a failed apply can never
  strand an unrevertable ruleset. Pending-change state persists across a
  restart, so the rollback still fires after the service restarts inside
  the confirmation window, and a pending timer can now be disarmed
  without settling the change either way. An isolated VLAN device is
  bound inside the template range it belongs to.
- **Agent and IPC.** The JSON-RPC client honours context cancellation,
  the frame size is bounded and read deadlines are set, and the caller's
  remaining timeout is carried across the RPC boundary instead of being
  dropped. Concurrent connections and subprocess execution are bounded.
- **Backup and restore.** Factory reset restores from the embedded copy
  rather than a directory that may not exist on the target. The export
  archive includes the OpenVPN PKI directory, each archived directory is
  restored to its real location, restored file permissions are clamped
  instead of trusted from the tar header, and config import caps both
  total entry count and cumulative size. A run aborted by a config guard
  now records a history entry instead of leaving the UI on the last
  success.
- **DNS.** The Unbound cache size is clamped to what the hardware can
  actually allocate. Tearing down the DoH plane reverses the apply
  order, so queries are not sent to a port that has already closed.
  Template identifiers corrupted by a bulk i18n replace are repaired.
- **VPN.** Peer tunnel IPs are allocated by scanning for the lowest free
  slot instead of appending, manual peer subnets are validated against
  the local networks, and a site-to-site invite's expiry is enforced at
  finalization rather than only at issue.
- **IPv6.** The 6in4 tunnel lifecycle honours the enabled flag, and the
  DHCPv6 lease watcher is tied to the shutdown context with its dispatch
  drained before the watcher stops.
- **Web and session layer.** The CSRF token is sent with every htmx
  request and rotated on authentication boundaries. Request logging
  moved outside the security middleware, response status is recorded in
  log lines, rate-limited responses carry `Retry-After`, and the
  enforced rate now matches the intended request budget. Concurrent
  event streams are capped and idle ones reaped; the rate limiter's
  cleanup ticker can be stopped. Background goroutines drain on
  shutdown. `base-uri` and `form-action` were added to the CSP, which do
  not inherit from `default-src` and were therefore unrestricted.
- **UI under the CSP.** The shipped policy sends `script-src 'self'`
  with no `'unsafe-inline'`, which silently disabled every inline
  handler in the templates; behaviour is now declared with data
  attributes and handled by a delegated listener. The vendored htmx
  bundle was a 212-byte placeholder, which left every `hx-post`,
  `hx-get`, `hx-delete` and `hx-confirm` inert with nothing reporting
  it; the real bundle is now vendored and pinned by digest. The theme
  preference is kept in the cookie alone.
- **Auth.** The cached password hash refreshes when the admin password
  changes, and failed logins are logged distinctly with progressive
  backoff.
- **Metrics.** The snapshot is cached instead of collected on every
  scrape, the OpenVPN active session count is collected, and the client
  `hostname` label was dropped since `mac` already keys the series.
- **NAS and storage.** Downloaded playlists and blocklists are size
  capped, M3U-derived paths are confined to the download directory, and
  a device and mount point are validated before an fstab entry is
  written.
- **Updates.** The OTA download and the extracted binary are both size
  capped, and a rollback is rejected when no update is pending.
- **Deployment and build.** `make install` builds for the host
  architecture, the service user is granted write access to the config
  directory (without which every runtime config write fails), the source
  Debian image is verified before extraction, the ISO builder bind mount
  is narrowed, the unused `qrencode` package is dropped and the ISO
  package lists closed, password root SSH is no longer enabled by
  default, and the checksums target no longer fails when no ISOs were
  built.
- **Other.** Health check monitoring goroutines start during server
  boot, per-target probe errors are logged instead of discarded, the
  health check partial that was written but unrouted is now reachable,
  handler error responses route through i18n, and `gofmt` is enforced by
  the linter rather than by habit.

### Security

- **Agent boundary.** The agent socket is restricted to root and the
  service user. Each allowed command is resolved to a trusted path: the
  caller's path string is discarded and the basename re-resolved against
  trusted bin directories, because validating a basename and then
  executing the caller's path made the checked string and the executed
  string different values, so any file named after an allowed command
  ran as root. `exec.run` no longer accepts a caller-supplied
  environment, since the loader honours `LD_PRELOAD` at execve time
  whatever binary the whitelist approved. Privileged stderr is kept out
  of browser responses.
- **Secrets at rest.** Backup target secrets and the archive passphrase
  are encrypted at rest, as are the WireGuard server and peer private
  keys. Responses carrying key material send `no-store`. Site-to-site
  tokens are signed with a dedicated key.
- **Input reaching a command or config template.** The OpenVPN client
  name is validated at every entry point, the settings domain is
  validated before it reaches a config template, the release tag is
  validated before it becomes a path, control characters are rejected in
  the NAS share path, and the M3U download path is cleaned before
  containment is enforced.
- **Outbound requests.** Outbound fetches reject internal destinations,
  and the DoT and DoH probes guard the address they actually dial rather
  than the hostname they were given, which also covers a redirect to an
  internal host and DNS rebinding.
- **Fail-closed behaviour.** The updater fails closed when the release
  checksum asset is missing, the pre-update snapshot is restricted and
  cleaned up, SFTP backup targets verify the host key against a pinned
  fingerprint, and the CSRF middleware fails closed when the CSPRNG read
  fails. CSRF tokens are compared in constant time.
- **Disclosure.** VPN peer names are hashed in the Prometheus
  exposition. That endpoint carries no authentication, and unlike a LAN
  client a remote peer is not otherwise observable from the local
  segment, so the exposition itself was the disclosure. Control
  characters are stripped from the request path before logging.
- **Supply chain.** `x/crypto` and `x/net` are bumped past their
  advisories, cached packages are re-resolved by version and hash,
  third-party actions are pinned to a commit SHA, lint and vulnerability
  tool versions are pinned, CI tracks Go patch releases instead of
  pinning one, and every `gosec` finding is triaged with the scanner
  wired into CI.
