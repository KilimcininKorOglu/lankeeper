package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/releasekey"
)

// writeKey stores a fresh seed the way generateKey does and returns its
// path and public half.
func writeKey(t *testing.T, dir string) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "release-signing.key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, pub
}

func writeSums(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(path, []byte("abc  lankeeper-v1.2.3-linux-amd64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSignWritesASignatureTheTrustedKeyVerifies(t *testing.T) {
	dir := t.TempDir()
	keyPath, pub := writeKey(t, dir)
	sumsPath := writeSums(t, dir)

	if err := sign(keyPath, sumsPath, pub); err != nil {
		t.Fatalf("sign: %v", err)
	}
	sums, _ := os.ReadFile(sumsPath)
	sig, err := os.ReadFile(sumsPath + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if err := releasekey.Verify(pub, sums, sig); err != nil {
		t.Errorf("the written signature does not verify: %v", err)
	}
}

// A secret holding any key but the release key would publish a release
// every router refuses, and an immutable release cannot be fixed later.
// Nothing is written, and an old signature beside the file is removed.
func TestSignRefusesAKeyTheRouterDoesNotTrust(t *testing.T) {
	dir := t.TempDir()
	keyPath, _ := writeKey(t, dir)
	sumsPath := writeSums(t, dir)
	if err := os.WriteFile(sumsPath+".sig", []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trusted, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	if err := sign(keyPath, sumsPath, trusted); err == nil {
		t.Fatal("a key other than the trusted one signed the release")
	}
	if _, err := os.Stat(sumsPath + ".sig"); !os.IsNotExist(err) {
		t.Errorf("a signature file was left behind: %v", err)
	}
}
