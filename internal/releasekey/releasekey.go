// Package releasekey holds the ed25519 public key that release
// signatures are checked against. The updater verifies every download
// with it, and the signing tool checks its own output with it, so a
// release signed with any other key is refused before it is published.
package releasekey

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// PublicKeyB64 is the public half of the release key. SHA256SUMS comes
// from the same GitHub Release as the archive it lists, so on its own it
// proves only that a download was not truncated: anyone able to edit the
// release can replace both. The detached signature over SHA256SUMS is
// what ties an archive to this key.
const PublicKeyB64 = "sI0C6bdE7SGWEL5zJ1ZkdwSdTIGcJsFlSvBwIbVjFPY="

var (
	// ErrMalformedSignature reports a signature file that does not hold
	// a base64 ed25519 signature.
	ErrMalformedSignature = errors.New("SHA256SUMS.sig is not a base64 ed25519 signature")
	// ErrBadSignature reports a signature made by another key or over
	// other bytes.
	ErrBadSignature = errors.New("SHA256SUMS signature does not verify against the release key")
)

// PublicKey returns the decoded release key. A malformed constant is a
// build defect, which the package test pins, so it panics rather than
// returning an error every caller would have to handle.
func PublicKey() ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(PublicKeyB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic(fmt.Sprintf("release key is not a base64 ed25519 public key: %v", err))
	}
	return ed25519.PublicKey(raw)
}

// Verify checks sigB64, a base64 detached signature as written to
// SHA256SUMS.sig, over the exact sums bytes against pub.
func Verify(pub ed25519.PublicKey, sums, sigB64 []byte) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrMalformedSignature
	}
	if !ed25519.Verify(pub, sums, sig) {
		return ErrBadSignature
	}
	return nil
}
