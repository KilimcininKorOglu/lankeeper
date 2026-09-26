// Command signrelease signs a release SHA256SUMS file with the release
// ed25519 key, or generates that key pair.
//
// The router verifies the detached signature against the public key
// compiled into the running binary before it installs an update, so the
// private key never enters the repository. Signing checks its own output
// against that same compiled-in key and writes nothing when they differ:
// a release signed with any other key is one every router refuses, and
// an immutable release cannot be corrected afterwards.
//
//	go run ./tools/signrelease -generate -key ~/.config/lankeeper/release-signing.key
//	go run ./tools/signrelease -key ~/.config/lankeeper/release-signing.key dist/SHA256SUMS
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/releasekey"
)

func main() {
	keyPath := flag.String("key", "", "path of the base64 ed25519 seed")
	generate := flag.Bool("generate", false, "create a new key pair at -key and print the public key")
	flag.Parse()

	var err error
	switch {
	case *keyPath == "":
		err = errors.New("-key is required")
	case *generate:
		err = generateKey(*keyPath)
	case flag.NArg() != 1:
		err = errors.New("usage: signrelease -key FILE SHA256SUMS")
	default:
		err = sign(*keyPath, flag.Arg(0), releasekey.PublicKey())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "signrelease:", err)
		os.Exit(1)
	}
}

// generateKey writes a new seed to path, refusing to replace an existing
// key, and prints the public key to embed in the binary.
func generateKey(path string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- operator-supplied key path
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

// sign writes the base64 signature of the file at sumsPath to
// sumsPath+".sig", after checking that it verifies against trusted. Any
// signature already beside sumsPath is removed first, so a failed run
// never leaves an old signature next to a new SHA256SUMS.
func sign(keyPath, sumsPath string, trusted ed25519.PublicKey) error {
	sigPath := sumsPath + ".sig"
	if err := os.Remove(sigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := os.ReadFile(keyPath) // #nosec G304 -- operator-supplied key path
	if err != nil {
		return err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("%s does not hold a base64 ed25519 seed", keyPath)
	}
	sums, err := os.ReadFile(sumsPath) // #nosec G304 -- operator-supplied release file
	if err != nil {
		return err
	}
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), sums)) + "\n")
	if err := releasekey.Verify(trusted, sums, sig); err != nil {
		return fmt.Errorf("the key at %s is not the release key compiled into the router: %w", keyPath, err)
	}
	// The signature is published with the release, so it is world-readable
	// on purpose.
	return os.WriteFile(sigPath, sig, 0o644) // #nosec G306 G703 -- operator-supplied release file
}
