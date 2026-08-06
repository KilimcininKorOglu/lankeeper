package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// useTempS2SKey points the token signing key at a temp file, since the
// production path lives under /var/lib.
func useTempS2SKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s2s-token.key")
	t.Setenv("LANKEEPER_S2S_KEY", path)
	return path
}

func newS2SKeyService(t *testing.T, sessionSecret string) *VPNService {
	t.Helper()
	cfg := &config.Config{}
	cfg.System.SessionSecret = sessionSecret
	return NewVPNService(cfg)
}

// TestSigningKeyIsNotTheSessionSecret is the regression test. The token
// signing key was []byte(System.SessionSecret), the same value that
// authenticates web session cookies. An invite token carries a
// WireGuard preshared key, so one disclosed secret let an attacker both
// forge session cookies and mint an invite that induces a peer into
// establishing a rogue tunnel. The two trust domains have to be
// independent.
func TestSigningKeyIsNotTheSessionSecret(t *testing.T) {
	useTempS2SKey(t)
	const secret = "0123456789abcdef0123456789abcdef"
	svc := newS2SKeyService(t, secret)

	key, err := svc.signingKey()
	if err != nil {
		t.Fatalf("signingKey: %v", err)
	}
	if string(key) == secret {
		t.Fatal("the token signing key is still the session secret")
	}
	if len(key) != 32 {
		t.Errorf("key length %d, want 32", len(key))
	}
}

// TestChangingTheSessionSecretDoesNotInvalidateTokens is the same
// property observed from the outside: rotating one domain's secret must
// not silently break the other.
func TestChangingTheSessionSecretDoesNotInvalidateTokens(t *testing.T) {
	useTempS2SKey(t)
	svc := newS2SKeyService(t, "original-session-secret-value-32b")

	token, err := svc.signToken(&S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "site-b"})
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	svc.cfg.System.SessionSecret = "a-completely-different-secret-32b"

	if _, err := svc.verifyToken(token); err != nil {
		t.Errorf("a session-secret change invalidated a site-to-site token: %v", err)
	}
}

// TestSigningKeyPersistsAcrossRestarts covers the reason the key is a
// file rather than a config field: the service account can write there
// even where router.yaml is not writable, so outstanding invites survive
// a restart instead of being silently invalidated by a regenerated
// secret.
func TestSigningKeyPersistsAcrossRestarts(t *testing.T) {
	path := useTempS2SKey(t)
	first := newS2SKeyService(t, "secret-one")

	token, err := first.signToken(&S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "site-b"})
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the key was not persisted: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode %o, want 600", perm)
	}

	// A fresh service stands in for a restarted process.
	second := newS2SKeyService(t, "secret-two")
	if _, err := second.verifyToken(token); err != nil {
		t.Errorf("a restart invalidated an outstanding token: %v", err)
	}
}

// TestRotateInvalidatesOutstandingTokens covers the rotation path the
// finding says was missing entirely: there was no way to respond to a
// suspected disclosure, or to revoke an invite already handed out.
func TestRotateInvalidatesOutstandingTokens(t *testing.T) {
	useTempS2SKey(t)
	svc := newS2SKeyService(t, "secret")

	token, err := svc.signToken(&S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "site-b"})
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}
	if _, err := svc.verifyToken(token); err != nil {
		t.Fatalf("the token did not verify before rotation: %v", err)
	}

	if err := svc.RotateS2SSigningKey(); err != nil {
		t.Fatalf("RotateS2SSigningKey: %v", err)
	}

	if _, err := svc.verifyToken(token); err == nil {
		t.Error("a token issued before rotation still verifies")
	}

	// Rotation has to leave the service able to issue usable tokens.
	fresh, err := svc.signToken(&S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "site-c"})
	if err != nil {
		t.Fatalf("signToken after rotation: %v", err)
	}
	if _, err := svc.verifyToken(fresh); err != nil {
		t.Errorf("a token issued after rotation does not verify: %v", err)
	}
}

// TestRotationSurvivesARestart makes sure the new key is the persisted
// one, not just an in-memory swap that a restart would undo.
func TestRotationSurvivesARestart(t *testing.T) {
	useTempS2SKey(t)
	svc := newS2SKeyService(t, "secret")

	token, err := svc.signToken(&S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "site-b"})
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}
	if err := svc.RotateS2SSigningKey(); err != nil {
		t.Fatalf("RotateS2SSigningKey: %v", err)
	}

	restarted := newS2SKeyService(t, "secret")
	if _, err := restarted.verifyToken(token); err == nil {
		t.Error("the pre-rotation token verifies again after a restart; the rotation was not persisted")
	}
}

// TestSigningKeyFailureIsReported keeps the error from being swallowed:
// signing with a key nobody can reproduce mints tokens that never
// verify, which is worse than refusing to sign.
func TestSigningKeyFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s2s-token.key")
	if err := os.WriteFile(path, []byte("not-hex"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	t.Setenv("LANKEEPER_S2S_KEY", path)

	svc := newS2SKeyService(t, "secret")
	_, err := svc.signToken(&S2SAck{Version: inviteSchemaVersion, Kind: tokenKindAck, Name: "site-b"})
	if err == nil {
		t.Fatal("an unreadable key produced a token anyway")
	}
	if !strings.Contains(err.Error(), "s2s token key") {
		t.Errorf("got %v, want an error naming the token key", err)
	}
}

// TestGeneratedKeyIsUniquePerRouter guards against a derivation that
// would make two routers share a key.
func TestGeneratedKeyIsUniquePerRouter(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a.key")
	b := filepath.Join(t.TempDir(), "b.key")

	t.Setenv("LANKEEPER_S2S_KEY", a)
	keyA, err := newS2SKeyService(t, "same-secret").signingKey()
	if err != nil {
		t.Fatalf("first key: %v", err)
	}

	t.Setenv("LANKEEPER_S2S_KEY", b)
	keyB, err := newS2SKeyService(t, "same-secret").signingKey()
	if err != nil {
		t.Fatalf("second key: %v", err)
	}

	if string(keyA) == string(keyB) {
		t.Error("two routers with the same session secret produced the same token key")
	}
	if _, err := config.LoadKey(a); err != nil {
		t.Errorf("the first key is not a valid stored key: %v", err)
	}
}
