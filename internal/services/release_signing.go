package services

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// releaseSigningKeyB64 is the ed25519 public key whose detached signature
// over SHA256SUMS every update must carry. The private half is kept
// outside the repository and used by tools/signrelease.
//
// SHA256SUMS comes from the same GitHub Release as the archive it lists,
// so on its own it proves only that the download was not truncated:
// anyone able to edit the release can replace both. The signature is
// what ties the archive to the maintainer's key.
const releaseSigningKeyB64 = "sI0C6bdE7SGWEL5zJ1ZkdwSdTIGcJsFlSvBwIbVjFPY="

// releaseSigningKey is a variable so tests can sign with a key of their
// own.
var releaseSigningKey = mustDecodeSigningKey(releaseSigningKeyB64)

func mustDecodeSigningKey(b64 string) ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic(fmt.Sprintf("release signing key is not a base64 ed25519 public key: %v", err))
	}
	return ed25519.PublicKey(raw)
}

// verifyChecksumSignature fetches the detached signature and checks it
// over the exact SHA256SUMS bytes. It fails closed: a release without a
// signature is refused.
func verifyChecksumSignature(ctx context.Context, sigURL string, sums []byte) error {
	if sigURL == "" {
		return errors.New("release has no SHA256SUMS.sig asset, refusing to install an unsigned binary")
	}
	raw, err := fetchChecksumFile(ctx, sigURL)
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("SHA256SUMS.sig is not a base64 ed25519 signature")
	}
	if !ed25519.Verify(releaseSigningKey, sums, sig) {
		return errors.New("SHA256SUMS signature does not verify against the release key")
	}
	return nil
}
