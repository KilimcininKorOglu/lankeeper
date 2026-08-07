// Package iso_test exercises the shell helpers in the ISO build script.
//
// The script has no test runner of its own, and `make test` is the gate
// this repository actually enforces, so driving bash from a Go test is
// what keeps the check covered rather than merely written.
package iso_test

import (
	"crypto/sha512"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The script exits before defining anything useful when it is run
// without arguments, so the function under test is extracted and sourced
// on its own. Everything from the definition to its closing brace is a
// self-contained unit: it reads only its argument, CHECKSUM_FILE and
// DEBIAN_ISO_SHA512.
func verifyFuncSource(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile("build-iso.sh")
	if err != nil {
		t.Fatalf("read build-iso.sh: %v", err)
	}
	body := string(raw)

	const start = "verify_source_iso() {"
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatal("verify_source_iso is gone from the build script")
	}
	rest := body[i:]
	// The function's closing brace is the first one at column zero.
	j := strings.Index(rest, "\n}\n")
	if j < 0 {
		t.Fatal("could not find the end of verify_source_iso")
	}
	return rest[:j+len("\n}\n")]
}

// runVerify sources the extracted function and calls it, returning the
// combined output and whether it accepted the image.
func runVerify(t *testing.T, isoPath, checksumFile, envDigest string) (string, bool) {
	t.Helper()

	script := "set -euo pipefail\n" +
		"CHECKSUM_FILE=" + shellQuote(checksumFile) + "\n" +
		verifyFuncSource(t) +
		"verify_source_iso " + shellQuote(isoPath) + "\n"

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "DEBIAN_ISO_SHA512="+envDigest)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeImage creates a stand-in for the source image and returns its
// digest. The content is arbitrary; only the digest matters to the
// function under test.
func writeImage(t *testing.T, dir, name, content string) (string, string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	sum := sha512.Sum512([]byte(content))
	return path, hex.EncodeToString(sum[:])
}

func writeChecksums(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "debian-images.sha512")
	body := "# comment line\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
	return path
}

// TestAnUnrecognisedImageIsRefused is the regression test. The source
// image was checked only for existence, then handed to xorriso (and to
// fdisk and dd at raw offsets on arm64) inside a container with the
// whole repository bind-mounted writable. The path is operator-supplied,
// so nothing established where the file came from.
func TestAnUnrecognisedImageIsRefused(t *testing.T) {
	dir := t.TempDir()
	iso, _ := writeImage(t, dir, "tampered.iso", "not the debian installer")
	_, knownDigest := writeImage(t, dir, "other.iso", "a different image")
	checksums := writeChecksums(t, dir, knownDigest+"  debian-known.iso")

	out, ok := runVerify(t, iso, checksums, "")
	if ok {
		t.Fatalf("an unlisted image was accepted:\n%s", out)
	}
	if !strings.Contains(out, "not one this build recognises") {
		t.Errorf("the refusal does not say why:\n%s", out)
	}
	// The operator has to be told how to proceed legitimately, or the
	// next step is to delete the check.
	if !strings.Contains(out, "gpg --verify") {
		t.Errorf("the refusal does not explain how to verify a new release:\n%s", out)
	}
}

// TestAListedImageIsAccepted keeps the guard from blocking the build it
// exists to protect.
func TestAListedImageIsAccepted(t *testing.T) {
	dir := t.TempDir()
	iso, digest := writeImage(t, dir, "debian.iso", "pretend installer contents")
	checksums := writeChecksums(t, dir, digest+"  debian-12.10.0-amd64-netinst.iso")

	out, ok := runVerify(t, iso, checksums, "")
	if !ok {
		t.Fatalf("a listed image was refused:\n%s", out)
	}
	if !strings.Contains(out, "known-good Debian digest") {
		t.Errorf("acceptance was not reported:\n%s", out)
	}
}

// TestTheFilenameDoesNotDecideAnything covers the matching rule: the
// build receives the image at whatever path the caller supplies, so a
// name-based match would be trivially defeated by renaming a file.
func TestTheFilenameDoesNotDecideAnything(t *testing.T) {
	dir := t.TempDir()
	iso, digest := writeImage(t, dir, "arbitrary-name.iso", "pretend installer contents")
	checksums := writeChecksums(t, dir, digest+"  debian-12.10.0-amd64-netinst.iso")

	if out, ok := runVerify(t, iso, checksums, ""); !ok {
		t.Errorf("a matching digest was refused because of the filename:\n%s", out)
	}

	// The converse: the right name with the wrong content is refused.
	renamed, _ := writeImage(t, dir, "debian-12.10.0-amd64-netinst.iso", "tampered")
	if out, ok := runVerify(t, renamed, checksums, ""); ok {
		t.Errorf("the expected filename was enough to pass:\n%s", out)
	}
}

// TestTheOverrideDemandsADigest covers the escape hatch. A maintainer
// building from a newer point release must still say what they expect;
// an option that simply skipped the check would leave the control in
// place in name only.
func TestTheOverrideDemandsADigest(t *testing.T) {
	dir := t.TempDir()
	iso, digest := writeImage(t, dir, "newer.iso", "a release the file does not list")
	checksums := writeChecksums(t, dir, strings.Repeat("0", 128)+"  unrelated.iso")

	out, ok := runVerify(t, iso, checksums, digest)
	if !ok {
		t.Fatalf("a correct override digest was refused:\n%s", out)
	}
	if !strings.Contains(out, "DEBIAN_ISO_SHA512") {
		t.Errorf("the override path was not reported:\n%s", out)
	}

	// A wrong override must fail, and must not fall through to the file.
	out, ok = runVerify(t, iso, checksums, strings.Repeat("a", 128))
	if ok {
		t.Errorf("a mismatched override digest was accepted:\n%s", out)
	}
	if !strings.Contains(out, "does not match DEBIAN_ISO_SHA512") {
		t.Errorf("the mismatch was not attributed to the override:\n%s", out)
	}
}

// TestTheOverrideIsCaseInsensitive accepts a digest pasted from a tool
// that prints uppercase hex, since the alternative is a maintainer
// concluding the check is broken.
func TestTheOverrideIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	iso, digest := writeImage(t, dir, "newer.iso", "contents")
	checksums := writeChecksums(t, dir, strings.Repeat("0", 128)+"  unrelated.iso")

	if out, ok := runVerify(t, iso, checksums, strings.ToUpper(digest)); !ok {
		t.Errorf("an uppercase override digest was refused:\n%s", out)
	}
}

// TestAMissingChecksumFileIsFatal keeps a deleted or mis-mounted file
// from quietly turning the check off.
func TestAMissingChecksumFileIsFatal(t *testing.T) {
	dir := t.TempDir()
	iso, _ := writeImage(t, dir, "debian.iso", "contents")

	out, ok := runVerify(t, iso, filepath.Join(dir, "absent.sha512"), "")
	if ok {
		t.Fatalf("a missing checksum file let the build continue:\n%s", out)
	}
	if !strings.Contains(out, "checksum file not found") {
		t.Errorf("the failure was not attributed to the missing file:\n%s", out)
	}
}

// TestTheShippedChecksumFileCoversBothDefaultImages ties the guard to
// what the Makefile actually builds. A checksum file that does not list
// the default images would fail every stock build.
func TestTheShippedChecksumFileCoversBothDefaultImages(t *testing.T) {
	raw, err := os.ReadFile("debian-images.sha512")
	if err != nil {
		t.Fatalf("read the shipped checksum file: %v", err)
	}
	body := string(raw)

	for _, want := range []string{
		"debian-12.10.0-amd64-netinst.iso",
		"debian-12.10.0-arm64-netinst.iso",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the checksum file does not list %s, which the Makefile defaults to", want)
		}
	}

	// Every digest line must carry a full SHA512, so a truncated paste
	// cannot silently match a prefix.
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		digest, _, found := strings.Cut(line, " ")
		if !found || len(digest) != 128 {
			t.Errorf("not a full sha512 digest line: %q", line)
			continue
		}
		if _, err := hex.DecodeString(digest); err != nil {
			t.Errorf("digest is not hex: %q", digest)
		}
	}
}
