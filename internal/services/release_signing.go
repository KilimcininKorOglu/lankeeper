package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/KilimcininKorOglu/lankeeper/internal/releasekey"
)

// releaseSigningKey is a variable so tests can sign with a key of their
// own.
var releaseSigningKey = releasekey.PublicKey()

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
	return releasekey.Verify(releaseSigningKey, sums, raw)
}
