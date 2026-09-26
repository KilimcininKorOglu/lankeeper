package releasekey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

// Every update is checked against this key, so a constant that does not
// decode would refuse every release.
func TestThePublicKeyDecodes(t *testing.T) {
	if len(PublicKey()) != ed25519.PublicKeySize {
		t.Fatal("release key has the wrong size")
	}
}

func TestVerifyAcceptsOnlyASignatureByTheGivenKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte("abc  lankeeper-v1.2.3-linux-amd64.tar.gz\n")
	good := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n"
	forged := base64.StdEncoding.EncodeToString(ed25519.Sign(other, sums))

	if err := Verify(pub, sums, []byte(good)); err != nil {
		t.Errorf("a valid signature was refused: %v", err)
	}
	if err := Verify(pub, append(sums, 'x'), []byte(good)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("an altered SHA256SUMS: err = %v, want ErrBadSignature", err)
	}
	if err := Verify(pub, sums, []byte(forged)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a signature by another key: err = %v, want ErrBadSignature", err)
	}
	if err := Verify(pub, sums, []byte("not a signature")); !errors.Is(err, ErrMalformedSignature) {
		t.Errorf("garbage: err = %v, want ErrMalformedSignature", err)
	}
}
