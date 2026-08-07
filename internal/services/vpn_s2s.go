package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// inviteSchemaVersion is bumped whenever the S2SInvite JSON shape
// changes. Consumers reject tokens with an unknown version so a
// downgraded LANKeeper does not silently misinterpret a newer token.
const inviteSchemaVersion = 1

// inviteDefaultTTL is how long a freshly issued join token stays
// valid. Long enough that an operator can switch between two
// browser tabs / devices, short enough that a leaked token is not
// indefinitely usable.
const inviteDefaultTTL = 60 * time.Minute

// inviteTokenSeparator splits the JSON body from the HMAC signature
// inside a token. Both halves are base64url with padding stripped.
const inviteTokenSeparator = "."

// ackKindInvite distinguishes the initial invite token from the ack
// token returned by the joining side. Both share the same JSON
// envelope and signature scheme but carry different payload fields.
const (
	tokenKindInvite = "invite"
	tokenKindAck    = "ack"
)

// S2SInvite is the payload exchanged between two LANKeepers when an
// operator runs the site-to-site wizard. It carries everything the
// joining side needs to add the originating router as a peer:
// public key, preshared key, tunnel endpoint and the LAN subnets
// the joining side will route through the tunnel.
//
// PSK is sent in plaintext inside the token. The token itself is
// HMAC-signed with the local token signing key to prevent tampering but
// is not encrypted; copy/paste it only over a trusted channel
// (LAN-only TLS UI, signal/email between admins, etc.). Tokens
// expire after inviteDefaultTTL by default.
type S2SInvite struct {
	Version         int       `json:"v"`
	Kind            string    `json:"kind"`
	Name            string    `json:"name"`
	SiteName        string    `json:"siteName,omitempty"`
	Endpoint        string    `json:"endpoint"`
	PublicKey       string    `json:"publicKey"`
	PresharedKey    string    `json:"presharedKey,omitempty"`
	TunnelIP        string    `json:"tunnelIP"`
	RemoteSubnets   []string  `json:"remoteSubnets"`
	ExpectedSubnets []string  `json:"expectedSubnets,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

// S2SAck is the reply token the joining side hands back to the
// originator so the originator can fill in the peer's public key
// and finalize the tunnel.
type S2SAck struct {
	Version   int       `json:"v"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`      // matches the invite Name
	PublicKey string    `json:"publicKey"` // joining side's public key
	CreatedAt time.Time `json:"createdAt"`
}

// Errors returned by token validation. Wrapped with %w so callers
// can branch on errors.Is.
var (
	ErrInviteExpired      = errors.New("s2s invite token expired")
	ErrInviteSignature    = errors.New("s2s invite signature invalid")
	ErrInviteSchema       = errors.New("s2s invite schema version unsupported")
	ErrInviteMalformed    = errors.New("s2s invite token malformed")
	ErrPeerNotPending     = errors.New("s2s peer is not in pending state")
	ErrPeerSubnetConflict = errors.New("s2s peer subnet conflicts with a local subnet")
	ErrPeerNameInUse      = errors.New("a peer with that name already exists")
)

// peerNameTakenLocked reports whether name is already claimed, pending
// peers included. Caller must hold s.mu.
//
// Both the manual Add Peer form and the invite wizard append to the same
// peer list, and every name-keyed lookup (RemovePeer, the client
// lookup) stops at the first match, so a duplicate makes those lookups
// ambiguous. One helper rather than two loops keeps the two paths from
// drifting apart again.
func (s *VPNService) peerNameTakenLocked(name string) bool {
	for _, existing := range s.cfg.VPN.Server.Peers {
		if existing.Name == name {
			return true
		}
	}
	return false
}

// s2sKeyPath resolves the token signing key location. It lives beside
// the credential encryption key rather than in router.yaml: the service
// account can write there, so the key survives a restart even where the
// config file itself is not writable, and nothing that copies or exports
// the config carries it along.
//
// The environment override exists for tests, which cannot write under
// /var/lib.
func s2sKeyPath() string {
	if p := os.Getenv("LANKEEPER_S2S_KEY"); p != "" {
		return p
	}
	return "/var/lib/lankeeper/credentials/s2s-token.key"
}

// signingKey returns the HMAC key used to sign site-to-site tokens,
// generating and persisting one on first use.
//
// This used to be System.SessionSecret, the same value that
// authenticates web session cookies. Those are two unrelated trust
// domains: an invite token carries a WireGuard preshared key, so one
// disclosed secret let an attacker both forge session cookies and
// induce a peer into establishing a rogue tunnel. They are now
// independent, and either can be rotated without touching the other.
//
// A failure to read or create the key is returned rather than papered
// over with a fallback: signing with a key nobody can reproduce would
// mint tokens that never verify, and verifying with one would accept
// nothing. Both are better reported than guessed at.
func (s *VPNService) signingKey() ([]byte, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	if len(s.s2sKey) > 0 {
		return s.s2sKey, nil
	}

	path := s2sKeyPath()
	key, err := config.LoadKey(path)
	if err == nil {
		s.s2sKey = key
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read s2s token key: %w", err)
	}

	key, err = config.GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create s2s key directory: %w", err)
	}
	if err := config.SaveKey(path, key); err != nil {
		return nil, fmt.Errorf("write s2s token key: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict s2s token key: %w", err)
	}

	log.Printf("vpn: generated a new site-to-site token signing key at %s", path)
	s.s2sKey = key
	return key, nil
}

// RotateS2SSigningKey replaces the token signing key, which is the
// response to a suspected disclosure. Every outstanding invite and ack
// token stops verifying immediately, so a rotation is also the way to
// revoke tokens that were handed out and should not be redeemed.
// Established tunnels are unaffected: they authenticate with WireGuard
// keys, not with these tokens.
func (s *VPNService) RotateS2SSigningKey() error {
	key, err := config.GenerateKey()
	if err != nil {
		return err
	}

	path := s2sKeyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create s2s key directory: %w", err)
	}
	if err := config.SaveKey(path, key); err != nil {
		return fmt.Errorf("write s2s token key: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict s2s token key: %w", err)
	}

	s.keyMu.Lock()
	s.s2sKey = key
	s.keyMu.Unlock()

	log.Printf("vpn: rotated the site-to-site token signing key; outstanding tokens no longer verify")
	return nil
}

// signToken serialises payload to JSON, HMAC-SHA256-signs it with
// the local secret, and returns the base64url(<json>.<sig>) token.
func (s *VPNService) signToken(payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal token: %w", err)
	}
	key, err := s.signingKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	sig := mac.Sum(nil)

	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + inviteTokenSeparator + enc.EncodeToString(sig), nil
}

// verifyToken parses the wire format, checks the HMAC, and returns
// the JSON body bytes. Callers unmarshal into the appropriate type.
func (s *VPNService) verifyToken(token string) ([]byte, error) {
	parts := strings.Split(strings.TrimSpace(token), inviteTokenSeparator)
	if len(parts) != 2 {
		return nil, ErrInviteMalformed
	}
	enc := base64.RawURLEncoding
	body, err := enc.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: body decode: %v", ErrInviteMalformed, err)
	}
	sig, err := enc.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: sig decode: %v", ErrInviteMalformed, err)
	}
	key, err := s.signingKey()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), sig) {
		return nil, ErrInviteSignature
	}
	return body, nil
}

// ParseInviteToken decodes and validates a join invite token.
// Returns the payload on success or one of ErrInvite* on failure.
func (s *VPNService) ParseInviteToken(token string) (*S2SInvite, error) {
	body, err := s.verifyToken(token)
	if err != nil {
		return nil, err
	}
	var inv S2SInvite
	if err := json.Unmarshal(body, &inv); err != nil {
		return nil, fmt.Errorf("%w: json: %v", ErrInviteMalformed, err)
	}
	if inv.Version != inviteSchemaVersion {
		return nil, fmt.Errorf("%w: got %d want %d", ErrInviteSchema, inv.Version, inviteSchemaVersion)
	}
	if inv.Kind != tokenKindInvite {
		return nil, fmt.Errorf("%w: kind %q", ErrInviteMalformed, inv.Kind)
	}
	if !inv.ExpiresAt.IsZero() && time.Now().After(inv.ExpiresAt) {
		return nil, ErrInviteExpired
	}
	return &inv, nil
}

// ParseAckToken decodes and validates a reply ack token.
func (s *VPNService) ParseAckToken(token string) (*S2SAck, error) {
	body, err := s.verifyToken(token)
	if err != nil {
		return nil, err
	}
	var ack S2SAck
	if err := json.Unmarshal(body, &ack); err != nil {
		return nil, fmt.Errorf("%w: json: %v", ErrInviteMalformed, err)
	}
	if ack.Version != inviteSchemaVersion {
		return nil, fmt.Errorf("%w: got %d want %d", ErrInviteSchema, ack.Version, inviteSchemaVersion)
	}
	if ack.Kind != tokenKindAck {
		return nil, fmt.Errorf("%w: kind %q", ErrInviteMalformed, ack.Kind)
	}
	return &ack, nil
}

// localSubnets returns the LAN-side CIDRs this router is willing to
// announce over a site-to-site tunnel: every interface with
// Role == "lan" plus the WireGuard server address itself.
func (s *VPNService) localSubnets() []string {
	var out []string
	for _, iface := range s.cfg.Interfaces {
		if iface.Role != "lan" || iface.Address == "" {
			continue
		}
		out = append(out, s.addressToSubnet(iface.Address))
	}
	if addr := s.cfg.VPN.Server.Address; addr != "" {
		out = append(out, s.addressToSubnet(addr))
	}
	return out
}

// nextTunnelIP picks the lowest unused /32 inside 10.10.11.0/24
// for a freshly issued peer. Skips the server address (.1) and any
// already-allocated peer.
func (s *VPNService) nextTunnelIP() (string, error) {
	used := map[string]struct{}{
		"10.10.11.1": {},
	}
	for _, p := range s.cfg.VPN.Server.Peers {
		ip, _, _ := strings.Cut(p.AllowedIPs, "/")
		ip = strings.SplitN(strings.TrimSpace(ip), ",", 2)[0]
		used[strings.TrimSpace(ip)] = struct{}{}
	}
	for n := 2; n < 255; n++ {
		candidate := fmt.Sprintf("10.10.11.%d", n)
		if _, taken := used[candidate]; !taken {
			return candidate + "/32", nil
		}
	}
	return "", errors.New("no free tunnel IPs in 10.10.11.0/24")
}

// subnetsConflict reports whether `remote` overlaps any of the
// local LAN subnets. Conflict means a S2S tunnel cannot route
// without NAT and we surface the error to the operator early.
func (s *VPNService) subnetsConflict(remote []string) (string, bool) {
	locals := s.localSubnets()
	for _, r := range remote {
		_, rNet, err := net.ParseCIDR(strings.TrimSpace(r))
		if err != nil {
			continue
		}
		for _, l := range locals {
			_, lNet, err := net.ParseCIDR(strings.TrimSpace(l))
			if err != nil {
				continue
			}
			if rNet.Contains(lNet.IP) || lNet.Contains(rNet.IP) {
				return r, true
			}
		}
	}
	return "", false
}

// CreateS2SInvite issues a new pending peer entry, generates the
// join token an operator can paste into the remote LANKeeper, and
// persists the pending peer record.
//
// peerName is operator-supplied, must be unique. expectedRemote is
// the LAN CIDR list the joining side advertises (validated for
// overlap; any overlap is rejected at this layer).
func (s *VPNService) CreateS2SInvite(
	ctx context.Context,
	peerName, siteName, endpoint string,
	expectedRemote []string,
) (token string, peer *config.WGServerPeer, err error) {
	if peerName == "" {
		return "", nil, errors.New("peer name required")
	}
	if endpoint == "" {
		return "", nil, errors.New("endpoint required")
	}
	if conflict, ok := s.subnetsConflict(expectedRemote); ok {
		return "", nil, fmt.Errorf("%w: %s", ErrPeerSubnetConflict, conflict)
	}

	psk, err := s.GeneratePresharedKey(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("psk: %w", err)
	}
	tunnelIP, err := s.nextTunnelIP()
	if err != nil {
		return "", nil, err
	}

	allowed := tunnelIP
	if len(expectedRemote) > 0 {
		allowed = tunnelIP + ", " + strings.Join(expectedRemote, ", ")
	}

	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(inviteDefaultTTL)

	pending := config.WGServerPeer{
		Name:            peerName,
		PresharedKey:    psk,
		AllowedIPs:      allowed,
		Keepalive:       25,
		RemoteSubnets:   append([]string(nil), expectedRemote...),
		IsSiteToSite:    true,
		Pending:         true,
		InviteExpiresAt: expires,
		// PublicKey deliberately empty until the ack arrives.
	}

	s.mu.Lock()
	if s.peerNameTakenLocked(peerName) {
		s.mu.Unlock()
		return "", nil, fmt.Errorf("%w: %s", ErrPeerNameInUse, peerName)
	}
	s.cfg.VPN.Server.Peers = append(s.cfg.VPN.Server.Peers, pending)
	s.mu.Unlock()

	if err := s.persist(); err != nil {
		return "", nil, fmt.Errorf("persist pending peer: %w", err)
	}

	inv := S2SInvite{
		Version:         inviteSchemaVersion,
		Kind:            tokenKindInvite,
		Name:            peerName,
		SiteName:        siteName,
		Endpoint:        endpoint,
		PublicKey:       s.cfg.VPN.Server.PublicKey,
		PresharedKey:    psk,
		TunnelIP:        tunnelIP,
		RemoteSubnets:   s.localSubnets(),
		ExpectedSubnets: append([]string(nil), expectedRemote...),
		CreatedAt:       now,
		ExpiresAt:       expires,
	}
	token, err = s.signToken(inv)
	if err != nil {
		return "", nil, err
	}
	// Return a snapshot of the peer so the handler doesn't have to
	// re-find it.
	saved := pending
	return token, &saved, nil
}

// ConsumeInvite is invoked on the joining side. It parses+verifies
// the incoming invite, generates a fresh keypair for the local
// side, registers the originating router as a (non-pending) peer,
// and returns the ack token + the peer's own public key so the
// operator can paste it back into the originator's wizard.
func (s *VPNService) ConsumeInvite(
	ctx context.Context,
	token string,
) (ackToken string, ourPubKey string, savedPeer *config.WGServerPeer, err error) {
	inv, err := s.ParseInviteToken(token)
	if err != nil {
		return "", "", nil, err
	}

	// The joining side's "remote subnets" are the originator's
	// local LAN subnets (RemoteSubnets in the invite payload).
	if conflict, ok := s.subnetsConflict(inv.RemoteSubnets); ok {
		return "", "", nil, fmt.Errorf("%w: %s", ErrPeerSubnetConflict, conflict)
	}

	priv, pub, err := s.GenerateKeypair(ctx)
	if err != nil {
		return "", "", nil, fmt.Errorf("genkey: %w", err)
	}
	_ = priv // private key is rendered into the joining side's wgs0.conf via cfg.VPN.Server.PrivateKey on its own router; here we simply add the originator as a peer.

	// Register the originating router as a peer on the joining
	// side. AllowedIPs = invite.TunnelIP + invite.RemoteSubnets.
	allowed := inv.TunnelIP
	if len(inv.RemoteSubnets) > 0 {
		allowed = inv.TunnelIP + ", " + strings.Join(inv.RemoteSubnets, ", ")
	}

	peer := config.WGServerPeer{
		Name:          inv.Name,
		PublicKey:     inv.PublicKey,
		PresharedKey:  inv.PresharedKey,
		AllowedIPs:    allowed,
		Keepalive:     25,
		Endpoint:      inv.Endpoint,
		RemoteSubnets: append([]string(nil), inv.RemoteSubnets...),
		IsSiteToSite:  true,
	}

	s.mu.Lock()
	for _, existing := range s.cfg.VPN.Server.Peers {
		if existing.Name == inv.Name {
			s.mu.Unlock()
			return "", "", nil, fmt.Errorf("peer %q already exists", inv.Name)
		}
	}
	s.cfg.VPN.Server.Peers = append(s.cfg.VPN.Server.Peers, peer)
	s.mu.Unlock()

	if err := s.persist(); err != nil {
		return "", "", nil, fmt.Errorf("persist peer: %w", err)
	}

	ack := S2SAck{
		Version:   inviteSchemaVersion,
		Kind:      tokenKindAck,
		Name:      inv.Name,
		PublicKey: pub,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	ackToken, err = s.signToken(ack)
	if err != nil {
		return "", "", nil, err
	}
	saved := peer
	return ackToken, pub, &saved, nil
}

// FinalizeInvite is invoked on the originating side once the
// operator pastes back the ack token from the joining router. It
// fills in the joining side's public key, clears Pending, and
// persists.
func (s *VPNService) FinalizeInvite(ctx context.Context, peerName, ackToken string) (*config.WGServerPeer, error) {
	ack, err := s.ParseAckToken(ackToken)
	if err != nil {
		return nil, err
	}
	if ack.Name != peerName {
		return nil, fmt.Errorf("%w: ack name %q does not match peer %q",
			ErrInviteMalformed, ack.Name, peerName)
	}

	s.mu.Lock()
	idx := -1
	for i, p := range s.cfg.VPN.Server.Peers {
		if p.Name == peerName {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return nil, fmt.Errorf("peer %q not found", peerName)
	}
	if !s.cfg.VPN.Server.Peers[idx].Pending {
		s.mu.Unlock()
		return nil, ErrPeerNotPending
	}
	// The Pending flag alone is not the expiry. It is cleared by the GC
	// ticker, which runs every five minutes, so trusting it let a leaked
	// or delayed invite become a permanent trusted peer for up to one
	// sweep past its deadline. The ack token carries no expiry of its
	// own, so this is the only place the originating side can enforce
	// the limit it published.
	if expires := s.cfg.VPN.Server.Peers[idx].InviteExpiresAt; !expires.IsZero() && time.Now().After(expires) {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: expired at %s", ErrInviteExpired, expires.UTC().Format(time.RFC3339))
	}
	s.cfg.VPN.Server.Peers[idx].PublicKey = ack.PublicKey
	s.cfg.VPN.Server.Peers[idx].Pending = false
	s.cfg.VPN.Server.Peers[idx].InviteExpiresAt = time.Time{}
	saved := s.cfg.VPN.Server.Peers[idx]
	s.mu.Unlock()

	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("persist finalize: %w", err)
	}
	return &saved, nil
}

// CancelInvite removes a pending peer (e.g. operator aborts the
// wizard before the ack arrives). Idempotent: returns nil if the
// peer is gone or was never pending.
func (s *VPNService) CancelInvite(peerName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.cfg.VPN.Server.Peers {
		if p.Name != peerName {
			continue
		}
		if !p.Pending {
			return ErrPeerNotPending
		}
		s.cfg.VPN.Server.Peers = append(s.cfg.VPN.Server.Peers[:i], s.cfg.VPN.Server.Peers[i+1:]...)
		return s.persist()
	}
	return nil
}

// GCExpiredInvites sweeps peers whose Pending invite has elapsed.
// Returns the number of peers reaped. Safe to call from a ticker.
func (s *VPNService) GCExpiredInvites() int {
	now := time.Now()
	s.mu.Lock()
	kept := s.cfg.VPN.Server.Peers[:0]
	reaped := 0
	for _, p := range s.cfg.VPN.Server.Peers {
		if p.Pending && !p.InviteExpiresAt.IsZero() && now.After(p.InviteExpiresAt) {
			reaped++
			continue
		}
		kept = append(kept, p)
	}
	s.cfg.VPN.Server.Peers = kept
	s.mu.Unlock()
	if reaped > 0 {
		_ = s.persist()
	}
	return reaped
}

// StartInviteGC launches a background goroutine that calls
// GCExpiredInvites every interval until ctx is done. interval <= 0
// defaults to 5 minutes.
//
// wg may be nil. When supplied, the goroutine is counted into it so
// shutdown can wait for it to drain.
func (s *VPNService) StartInviteGC(ctx context.Context, interval time.Duration, wg *sync.WaitGroup) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		// Sweep once at startup so a long downtime doesn't keep
		// stale invites lying around.
		s.GCExpiredInvites()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.GCExpiredInvites()
			}
		}
	}()
}

// SyncWGServer reloads the running wgs0 interface in place using
// `wg syncconf` so a peer add/remove does not tear down the
// existing tunnel. Falls back to ServerDown+ServerUp if syncconf
// fails (e.g. when address/port changed).
func (s *VPNService) SyncWGServer(ctx context.Context) error {
	if err := s.RenderServerConfig(ctx); err != nil {
		return err
	}
	// `wg-quick strip` emits a kernel-friendly form of the config
	// (no PostUp/PostDown, no Address). Pipe into wg syncconf.
	stripped, err := netutil.RunSimple(ctx, "wg-quick", "strip", "wgs0")
	if err != nil {
		return fmt.Errorf("wg-quick strip: %w", err)
	}
	tmpPath := "/tmp/lankeeper-wgs0-sync.conf"
	if err := netutil.WriteFile(tmpPath, []byte(stripped), 0o600); err != nil {
		return fmt.Errorf("write stripped: %w", err)
	}
	if _, err := netutil.Run(ctx, "wg", "syncconf", "wgs0", tmpPath); err != nil {
		return fmt.Errorf("wg syncconf: %w", err)
	}
	return nil
}

// S2SHealthInfo summarises the runtime state of one site-to-site
// peer for the dashboard.
type S2SHealthInfo struct {
	Name             string
	Online           bool
	HandshakeAgeSec  int64 // -1 when never handshaken
	RxBytes, TxBytes uint64
	Endpoint         string
	RemoteSubnets    []string
}

// S2SHealth queries `wg show wgs0 dump` and projects it onto the
// pending+active S2S peers in cfg.
func (s *VPNService) S2SHealth(ctx context.Context, peerName string) (*S2SHealthInfo, error) {
	out, err := netutil.RunSimple(ctx, "wg", "show", "wgs0", "dump")
	if err != nil {
		return nil, fmt.Errorf("wg show dump: %w", err)
	}
	// wg show dump format (peer rows tab-separated):
	// pubkey  psk  endpoint  allowed_ips  latest_handshake  rx_bytes  tx_bytes  keepalive
	wantPeer := s.findS2SPeer(peerName)
	if wantPeer == nil {
		return nil, fmt.Errorf("s2s peer %q not found", peerName)
	}
	info := &S2SHealthInfo{
		Name:            peerName,
		HandshakeAgeSec: -1,
		Endpoint:        wantPeer.Endpoint,
		RemoteSubnets:   append([]string(nil), wantPeer.RemoteSubnets...),
	}
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 8 {
			continue
		}
		if fields[0] != wantPeer.PublicKey {
			continue
		}
		var hs int64
		_, _ = fmt.Sscanf(fields[4], "%d", &hs)
		if hs > 0 {
			info.HandshakeAgeSec = time.Now().Unix() - hs
			info.Online = info.HandshakeAgeSec < 180
		}
		_, _ = fmt.Sscanf(fields[5], "%d", &info.RxBytes)
		_, _ = fmt.Sscanf(fields[6], "%d", &info.TxBytes)
		break
	}
	return info, nil
}

// S2SReachability fires a single ICMP echo to the .1 address of
// the first remote subnet via the wgs0 interface. Bounded to a 2s
// total budget so the UI doesn't hang.
func (s *VPNService) S2SReachability(ctx context.Context, peerName string) error {
	peer := s.findS2SPeer(peerName)
	if peer == nil {
		return fmt.Errorf("s2s peer %q not found", peerName)
	}
	if len(peer.RemoteSubnets) == 0 {
		return errors.New("peer has no remote subnets")
	}
	target, err := gatewayOfSubnet(peer.RemoteSubnets[0])
	if err != nil {
		return err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = netutil.RunSimple(pingCtx, "ping", "-c", "1", "-W", "2", "-I", "wgs0", target)
	if err != nil {
		return fmt.Errorf("ping %s via wgs0: %w", target, err)
	}
	return nil
}

// findS2SPeer returns the named site-to-site peer (active or
// pending) or nil. Caller must not mutate; the slice is shared.
func (s *VPNService) findS2SPeer(name string) *config.WGServerPeer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, p := range s.cfg.VPN.Server.Peers {
		if p.Name == name && p.IsSiteToSite {
			return &s.cfg.VPN.Server.Peers[i]
		}
	}
	return nil
}

// gatewayOfSubnet returns the first usable IP of the given CIDR
// (i.e. the .1 of a /24, .1 of a /16). Used as the ping target for
// reachability checks since router IPs by convention sit on .1.
func gatewayOfSubnet(cidr string) (string, error) {
	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return "", fmt.Errorf("parse cidr: %w", err)
	}
	_ = ip
	gw := make(net.IP, len(ipNet.IP))
	copy(gw, ipNet.IP)
	gw[len(gw)-1] |= 1
	return gw.String(), nil
}
