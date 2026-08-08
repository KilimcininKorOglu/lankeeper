# Changelog

All notable changes to LANKeeper are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/).

Entries start again from this point. Release notes for v0.5.0 and
earlier live on their GitHub Releases, and the full detail is in the
git history.

## [Unreleased]

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
