package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// inviteSchemaVersion is bumped whenever the invite or ack JSON shape
// changes. Consumers reject tokens with an unknown version so a
// downgraded LANKeeper does not silently misinterpret a newer token.
//
// Version 1 signed both tokens with a key only the issuing router
// held, so the other router could never verify them.
const inviteSchemaVersion = 2

// inviteDefaultTTL is how long a freshly issued join token stays
// valid. Long enough that an operator can switch between two
// browser tabs / devices, short enough that a leaked token is not
// indefinitely usable.
const inviteDefaultTTL = 60 * time.Minute

// ackMACSeparator splits an ack token's JSON body from its MAC. Both
// halves are base64url with padding stripped.
const ackMACSeparator = "."

// tokenKindInvite and tokenKindAck distinguish the invite token from
// the ack token returned by the joining side, so neither can be pasted
// where the other is expected.
const (
	tokenKindInvite = "invite"
	tokenKindAck    = "ack"
)

// s2sNamePattern bounds a peer name. The name is written into the
// rendered wgs0.conf, so a newline in it would add lines to that file.
var s2sNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// S2SInvite is the payload exchanged between two LANKeepers when an
// operator runs the site-to-site wizard. It carries everything the
// joining side needs to add the originating router as a peer:
// public key, preshared key, tunnel endpoint and the LAN subnets
// the joining side will route through the tunnel.
//
// The invite is not signed. Two routers share no secret before the
// wizard runs, so nothing the originator could attach would be
// verifiable by the joining side; its authenticity rests on the channel
// the operator copies it over, exactly as for a WireGuard config file.
// It carries the preshared key in plaintext, so that channel must also
// be private. Every field is validated on arrival, because the joining
// side writes them into its WireGuard config as root.
type S2SInvite struct {
	Version         int       `json:"v"`
	Kind            string    `json:"kind"`
	Name            string    `json:"name"`
	SiteName        string    `json:"siteName,omitempty"`
	Endpoint        string    `json:"endpoint"`
	PublicKey       string    `json:"publicKey"`
	PresharedKey    string    `json:"presharedKey,omitempty"`
	RemoteSubnets   []string  `json:"remoteSubnets"`
	ExpectedSubnets []string  `json:"expectedSubnets,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

// S2SAck is the reply token the joining side hands back to the
// originator so the originator can fill in the peer's public key
// and finalize the tunnel.
//
// The ack is MACed with the invite's preshared key. That key is unique
// to one invite and known only to the two routers, so a valid MAC
// proves the ack answers that invite and was made by someone who read
// it.
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
	ErrInviteExpired       = errors.New("s2s invite token expired")
	ErrInviteSignature     = errors.New("s2s invite signature invalid")
	ErrInviteSchema        = errors.New("s2s invite schema version unsupported")
	ErrInviteMalformed     = errors.New("s2s invite token malformed")
	ErrPeerNotPending      = errors.New("s2s peer is not in pending state")
	ErrPeerSubnetConflict  = errors.New("s2s peer subnet conflicts with a local subnet")
	ErrPeerNameInUse       = errors.New("a peer with that name already exists")
	ErrPeerSubnetMismatch  = errors.New("s2s invite expects LAN subnets this router does not have")
	ErrS2SServerKeyMissing = errors.New("the WireGuard server has no key pair; set up the server before a site-to-site link")
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

// encodeInvite serialises an invite as unpadded base64url JSON.
func encodeInvite(inv *S2SInvite) (string, error) {
	body, err := json.Marshal(inv)
	if err != nil {
		return "", fmt.Errorf("marshal invite: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

// ParseInviteToken decodes a join invite token and validates every
// field the joining side will write into its WireGuard config. Returns
// the payload on success or one of ErrInvite* on failure.
func ParseInviteToken(token string) (*S2SInvite, error) {
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrInviteMalformed, err)
	}
	var inv S2SInvite
	if err := json.Unmarshal(body, &inv); err != nil {
		return nil, fmt.Errorf("%w: json: %v", ErrInviteMalformed, err)
	}
	if err := checkTokenHeader(inv.Version, inv.Kind, tokenKindInvite); err != nil {
		return nil, err
	}
	if !inv.ExpiresAt.IsZero() && time.Now().After(inv.ExpiresAt) {
		return nil, ErrInviteExpired
	}
	if err := validateInvite(&inv); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInviteMalformed, err)
	}
	return &inv, nil
}

// checkTokenHeader refuses a token of another schema version or kind.
func checkTokenHeader(version int, kind, want string) error {
	if version != inviteSchemaVersion {
		return fmt.Errorf("%w: got %d want %d", ErrInviteSchema, version, inviteSchemaVersion)
	}
	if kind != want {
		return fmt.Errorf("%w: kind %q", ErrInviteMalformed, kind)
	}
	return nil
}

// validateInvite checks the fields that reach the rendered wgs0.conf.
// The invite is unsigned, so this is the only thing standing between
// its text and a config file that wg-quick runs as root.
func validateInvite(inv *S2SInvite) error {
	if !s2sNamePattern.MatchString(inv.Name) {
		return fmt.Errorf("invalid peer name %q", inv.Name)
	}
	if err := validateWGKey(inv.PublicKey); err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	if err := validateWGKey(inv.PresharedKey); err != nil {
		return fmt.Errorf("preshared key: %w", err)
	}
	if err := validateS2SEndpoint(inv.Endpoint); err != nil {
		return err
	}
	// Either list empty would render a peer with no AllowedIPs, which
	// routes nothing and which wg-quick refuses to parse.
	if len(inv.RemoteSubnets) == 0 || len(inv.ExpectedSubnets) == 0 {
		return errors.New("the invite names no LAN subnets")
	}
	return validateSubnetList(append(slices.Clone(inv.RemoteSubnets), inv.ExpectedSubnets...))
}

// validateWGKey accepts a WireGuard key: 32 bytes in standard base64.
func validateWGKey(key string) error {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return errors.New("not a WireGuard key")
	}
	return nil
}

// validateS2SEndpoint accepts host:port where host is an IP address or
// a DNS name.
func validateS2SEndpoint(endpoint string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint %q: %w", endpoint, err)
	}
	if n, err := strconv.Atoi(port); err != nil || netutil.ValidatePort(n) != nil {
		return fmt.Errorf("invalid endpoint port %q", port)
	}
	if net.ParseIP(host) == nil && ValidateDomain(host) != nil {
		return fmt.Errorf("invalid endpoint host %q", host)
	}
	return nil
}

// ValidatePeerEndpoint checks a manually entered peer endpoint. The value
// lands on the Endpoint line of wgs0.conf, which wg-quick runs as root, so
// a line break in it would add a section with a PostUp hook.
func ValidatePeerEndpoint(endpoint string) error {
	return validateS2SEndpoint(endpoint)
}

// validateSubnetList accepts only CIDRs.
func validateSubnetList(subnets []string) error {
	for _, cidr := range subnets {
		if err := netutil.ValidateCIDR(cidr); err != nil {
			return err
		}
	}
	return nil
}

// signAck serialises an ack and appends its MAC under the invite's
// preshared key.
func signAck(ack *S2SAck, psk string) (string, error) {
	body, err := json.Marshal(ack)
	if err != nil {
		return "", fmt.Errorf("marshal ack: %w", err)
	}
	mac, err := ackMAC(body, psk)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + ackMACSeparator + enc.EncodeToString(mac), nil
}

// ackMAC is HMAC-SHA256 over body keyed with the decoded preshared key.
func ackMAC(body []byte, psk string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(psk)
	if err != nil || len(key) != 32 {
		return nil, errors.New("the preshared key is not a WireGuard key")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return mac.Sum(nil), nil
}

// ParseAckToken decodes a reply ack token without verifying it. The key
// that verifies it is the preshared key of the pending peer the ack
// names, so the caller looks that peer up and calls verifyAck.
func ParseAckToken(token string) (ack *S2SAck, body, mac []byte, err error) {
	bodyPart, macPart, ok := strings.Cut(strings.TrimSpace(token), ackMACSeparator)
	if !ok {
		return nil, nil, nil, ErrInviteMalformed
	}
	enc := base64.RawURLEncoding
	if body, err = enc.DecodeString(bodyPart); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: body decode: %v", ErrInviteMalformed, err)
	}
	if mac, err = enc.DecodeString(macPart); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: mac decode: %v", ErrInviteMalformed, err)
	}
	ack = &S2SAck{}
	if err := json.Unmarshal(body, ack); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: json: %v", ErrInviteMalformed, err)
	}
	if err := checkTokenHeader(ack.Version, ack.Kind, tokenKindAck); err != nil {
		return nil, nil, nil, err
	}
	if err := validateWGKey(ack.PublicKey); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: public key: %v", ErrInviteMalformed, err)
	}
	return ack, body, mac, nil
}

// verifyAck checks an ack's MAC against the pending peer's preshared key.
func verifyAck(body, mac []byte, psk string) error {
	want, err := ackMAC(body, psk)
	if err != nil {
		return err
	}
	if !hmac.Equal(want, mac) {
		return ErrInviteSignature
	}
	return nil
}

// lanSubnets returns the networks of every interface with Role "lan".
// These are what a site-to-site link announces and routes.
//
// The WireGuard server subnet is not among them. Every LANKeeper ships
// with the same one, so announcing it made two default routers conflict
// on the first join, and the far side could not route it anyway while
// its own road-warrior peers used the same addresses.
func (s *VPNService) lanSubnets() []string {
	var out []string
	for _, iface := range s.cfg.Interfaces {
		if iface.Role != "lan" || iface.Address == "" {
			continue
		}
		out = append(out, s.addressToSubnet(iface.Address))
	}
	return out
}

// reservedSubnets is every network this router already routes locally:
// its LANs plus the WireGuard server subnet. A peer may claim none of
// them.
func (s *VPNService) reservedSubnets() []string {
	out := s.lanSubnets()
	if addr := s.cfg.VPN.Server.Address; addr != "" {
		out = append(out, s.addressToSubnet(addr))
	}
	return out
}

// sameSubnets reports whether two CIDR lists name the same set of
// networks, ignoring order and host bits.
func sameSubnets(a, b []string) bool {
	canon := func(list []string) []string {
		out := make([]string, 0, len(list))
		for _, cidr := range list {
			if _, n, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil {
				out = append(out, n.String())
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(canon(a), canon(b))
}

// nextTunnelIP picks the lowest free host address in the server's
// tunnel subnet for a freshly issued peer. It skips the network and
// broadcast addresses, the server's own address and every allocated
// peer.
func (s *VPNService) nextTunnelIP() (string, error) {
	serverIP, pool, err := net.ParseCIDR(strings.TrimSpace(s.cfg.VPN.Server.Address))
	if err != nil {
		return "", fmt.Errorf("parse WireGuard server address %q: %w", s.cfg.VPN.Server.Address, err)
	}
	used := s.usedTunnelIPs()
	used[serverIP.String()] = struct{}{}
	for ip := nextIP(pool.IP); pool.Contains(ip); ip = nextIP(ip) {
		if !pool.Contains(nextIP(ip)) {
			break // the broadcast address
		}
		if _, taken := used[ip.String()]; !taken {
			return ip.String() + "/32", nil
		}
	}
	return "", fmt.Errorf("no free tunnel IPs in %s", pool)
}

// usedTunnelIPs collects the tunnel address of every peer, which is
// the first entry of its AllowedIPs.
func (s *VPNService) usedTunnelIPs() map[string]struct{} {
	used := map[string]struct{}{}
	for _, p := range s.cfg.VPN.Server.Peers {
		first, _, _ := strings.Cut(p.AllowedIPs, ",")
		ip, _, _ := strings.Cut(strings.TrimSpace(first), "/")
		used[ip] = struct{}{}
	}
	return used
}

// nextIP returns the address after ip.
func nextIP(ip net.IP) net.IP {
	out := slices.Clone(ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}

// subnetsConflict reports whether `remote` overlaps any network this
// router routes locally. Conflict means a S2S tunnel cannot route
// without NAT and we surface the error to the operator early.
func (s *VPNService) subnetsConflict(remote []string) (string, bool) {
	locals := s.reservedSubnets()
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
	if err := s.validateInviteRequest(peerName, endpoint, expectedRemote); err != nil {
		return "", nil, err
	}

	psk, err := s.GeneratePresharedKey(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("psk: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(inviteDefaultTTL)

	// A site-to-site peer gets no tunnel address: the link carries LAN
	// to LAN traffic only, so it routes exactly the far side's LANs.
	pending := config.WGServerPeer{
		Name:            peerName,
		PresharedKey:    psk,
		AllowedIPs:      strings.Join(expectedRemote, ", "),
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
		RemoteSubnets:   s.lanSubnets(),
		ExpectedSubnets: append([]string(nil), expectedRemote...),
		CreatedAt:       now,
		ExpiresAt:       expires,
	}
	token, err = encodeInvite(&inv)
	if err != nil {
		return "", nil, err
	}
	// Return a snapshot of the peer so the handler doesn't have to
	// re-find it.
	saved := pending
	return token, &saved, nil
}

// validateInviteRequest checks what the originating side is about to
// put into an invite: the peer name and endpoint the joining side will
// write into its config, a server key pair to hand out, and remote
// subnets that do not overlap this router's own.
func (s *VPNService) validateInviteRequest(peerName, endpoint string, expectedRemote []string) error {
	if !s2sNamePattern.MatchString(peerName) {
		return fmt.Errorf("invalid peer name %q", peerName)
	}
	if err := validateS2SEndpoint(endpoint); err != nil {
		return err
	}
	if s.cfg.VPN.Server.PublicKey == "" {
		return ErrS2SServerKeyMissing
	}
	if len(expectedRemote) == 0 || len(s.lanSubnets()) == 0 {
		return errors.New("a site-to-site link needs a LAN subnet on each side")
	}
	if conflict, ok := s.subnetsConflict(expectedRemote); ok {
		return fmt.Errorf("%w: %s", ErrPeerSubnetConflict, conflict)
	}
	return nil
}

// ConsumeInvite is invoked on the joining side. It parses and validates
// the incoming invite, registers the originating router as a
// (non-pending) peer, and returns the ack token and this router's
// server public key so the operator can paste the ack back into the
// originator's wizard.
//
// The key handed back is the server's own, because wgs0 is the
// interface the tunnel runs on and it authenticates with the server key
// pair. Any other key would name a private key nobody holds.
func (s *VPNService) ConsumeInvite(
	_ context.Context,
	token string,
) (ackToken string, ourPubKey string, savedPeer *config.WGServerPeer, err error) {
	inv, err := ParseInviteToken(token)
	if err != nil {
		return "", "", nil, err
	}
	pub := s.cfg.VPN.Server.PublicKey
	if pub == "" {
		return "", "", nil, ErrS2SServerKeyMissing
	}
	if err := s.checkInviteSubnets(inv); err != nil {
		return "", "", nil, err
	}

	// Register the originating router as a peer on the joining side,
	// routing exactly the originator's LANs.
	peer := config.WGServerPeer{
		Name:          inv.Name,
		PublicKey:     inv.PublicKey,
		PresharedKey:  inv.PresharedKey,
		AllowedIPs:    strings.Join(inv.RemoteSubnets, ", "),
		Keepalive:     25,
		Endpoint:      inv.Endpoint,
		RemoteSubnets: append([]string(nil), inv.RemoteSubnets...),
		IsSiteToSite:  true,
	}

	s.mu.Lock()
	if s.peerNameTakenLocked(inv.Name) {
		s.mu.Unlock()
		return "", "", nil, fmt.Errorf("%w: %s", ErrPeerNameInUse, inv.Name)
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
	ackToken, err = signAck(&ack, inv.PresharedKey)
	if err != nil {
		return "", "", nil, err
	}
	saved := peer
	return ackToken, pub, &saved, nil
}

// checkInviteSubnets refuses an invite whose subnets cannot route. The
// originator expects this router to announce ExpectedSubnets and will
// accept traffic only from them, so any difference from this router's
// real LANs is a link that comes up and silently drops traffic. The
// originator's own LANs (RemoteSubnets) must not overlap anything this
// router already routes.
func (s *VPNService) checkInviteSubnets(inv *S2SInvite) error {
	if local := s.lanSubnets(); !sameSubnets(inv.ExpectedSubnets, local) {
		return fmt.Errorf("%w: the invite expects %s, this router's LANs are %s",
			ErrPeerSubnetMismatch, strings.Join(inv.ExpectedSubnets, ", "), strings.Join(local, ", "))
	}
	if conflict, ok := s.subnetsConflict(inv.RemoteSubnets); ok {
		return fmt.Errorf("%w: %s", ErrPeerSubnetConflict, conflict)
	}
	return nil
}

// FinalizeInvite is invoked on the originating side once the
// operator pastes back the ack token from the joining router. It
// verifies the ack against the pending peer's preshared key, fills in
// the joining side's public key, clears Pending, and persists.
func (s *VPNService) FinalizeInvite(_ context.Context, peerName, ackToken string) (*config.WGServerPeer, error) {
	ack, body, mac, err := ParseAckToken(ackToken)
	if err != nil {
		return nil, err
	}
	if ack.Name != peerName {
		return nil, fmt.Errorf("%w: ack name %q does not match peer %q",
			ErrInviteMalformed, ack.Name, peerName)
	}

	s.mu.Lock()
	idx, err := s.pendingPeerIndexLocked(peerName)
	if err == nil {
		err = verifyAck(body, mac, s.cfg.VPN.Server.Peers[idx].PresharedKey)
	}
	if err != nil {
		s.mu.Unlock()
		return nil, err
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

// pendingPeerIndexLocked finds the named peer and checks it is still an
// unexpired pending invite. Caller must hold s.mu.
//
// The Pending flag alone is not the expiry. It is cleared by the GC
// ticker, which runs every five minutes, so trusting it let a leaked or
// delayed invite become a permanent trusted peer for up to one sweep
// past its deadline. The ack token carries no expiry of its own, so
// this is the only place the originating side can enforce the limit it
// published.
func (s *VPNService) pendingPeerIndexLocked(name string) (int, error) {
	for i, p := range s.cfg.VPN.Server.Peers {
		if p.Name != name {
			continue
		}
		if !p.Pending {
			return -1, ErrPeerNotPending
		}
		if !p.InviteExpiresAt.IsZero() && time.Now().After(p.InviteExpiresAt) {
			return -1, fmt.Errorf("%w: expired at %s", ErrInviteExpired, p.InviteExpiresAt.UTC().Format(time.RFC3339))
		}
		return i, nil
	}
	return -1, fmt.Errorf("peer %q not found", name)
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

// S2SReachability fires a single ICMP echo from this router's LAN
// address to the .1 address of the first remote subnet. Bounded to a
// 3s total budget so the UI doesn't hang.
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
	source, err := s.lanSourceIP()
	if err != nil {
		return err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = netutil.RunSimple(pingCtx, "ping", "-c", "1", "-W", "2", "-I", source, target)
	if err != nil {
		return fmt.Errorf("ping %s from %s: %w", target, source, err)
	}
	return nil
}

// lanSourceIP returns the address of the first LAN interface. The far
// side accepts tunnel traffic only from this router's LANs, so a probe
// sent from the wgs0 address would be dropped by WireGuard there.
func (s *VPNService) lanSourceIP() (string, error) {
	for _, iface := range s.cfg.Interfaces {
		if iface.Role != "lan" || iface.Address == "" {
			continue
		}
		ip, _, err := net.ParseCIDR(strings.TrimSpace(iface.Address))
		if err != nil {
			return "", fmt.Errorf("parse LAN address %q: %w", iface.Address, err)
		}
		return ip.String(), nil
	}
	return "", errors.New("no LAN interface address to send the probe from")
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
