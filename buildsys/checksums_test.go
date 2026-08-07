// Package buildsys_test exercises Makefile targets that gate a release.
//
// The Makefile has no test runner of its own, and `make test` is the
// gate this repository enforces, so driving make from a Go test is what
// keeps a release recipe covered rather than merely written.
package buildsys_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the directory make has to run from.
const repoRoot = ".."

// runChecksums invokes the real target for a version nothing else uses,
// so it cannot collide with a genuine build.
func runChecksums(t *testing.T, version string) (string, error) {
	t.Helper()

	cmd := exec.Command("make", "checksums", "VERSION="+version)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// stageArtifact writes a stand-in release artifact and removes it after
// the test. Only the name matters; the recipe hashes whatever it finds.
func stageArtifact(t *testing.T, name string) {
	t.Helper()

	path := filepath.Join(repoRoot, "dist", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(path, []byte("stand-in artifact\n"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

// preserveChecksums keeps a real SHA256SUMS from being lost to a test
// run, since the recipe writes to a fixed path.
func preserveChecksums(t *testing.T) {
	t.Helper()

	path := filepath.Join(repoRoot, "dist", "SHA256SUMS")
	saved, err := os.ReadFile(path)
	existed := err == nil

	t.Cleanup(func() {
		if existed {
			_ = os.WriteFile(path, saved, 0o600)
			return
		}
		_ = os.Remove(path)
	})
}

// TestChecksumsSucceedsWithoutISOs is the regression test. The recipe
// looped `[ -f "$f" ] && shasum ...`, which made the loop's exit status
// the status of its last iteration. `make release` builds tarballs and
// no ISOs, so the ISO pattern never matched, the final test was false,
// and the target failed after having written a perfectly good
// SHA256SUMS. The documented archives-only release therefore could not
// complete.
func TestChecksumsSucceedsWithoutISOs(t *testing.T) {
	preserveChecksums(t)

	const version = "v0.0.0-checksumtest"
	stageArtifact(t, "lankeeper-"+version+"-linux-amd64.tar.gz")
	stageArtifact(t, "lankeeper-"+version+"-linux-arm64.tar.gz")

	out, err := runChecksums(t, version)
	if err != nil {
		t.Fatalf("make checksums failed with tarballs but no ISOs: %v\n%s", err, out)
	}

	raw, readErr := os.ReadFile(filepath.Join(repoRoot, "dist", "SHA256SUMS"))
	if readErr != nil {
		t.Fatalf("read SHA256SUMS: %v", readErr)
	}
	body := string(raw)
	for _, arch := range []string{"amd64", "arm64"} {
		want := "lankeeper-" + version + "-linux-" + arch + ".tar.gz"
		if !strings.Contains(body, want) {
			t.Errorf("SHA256SUMS does not list %s:\n%s", want, body)
		}
	}
	if lines := strings.Count(strings.TrimSpace(body), "\n") + 1; lines != 2 {
		t.Errorf("SHA256SUMS holds %d lines, want one per staged artifact:\n%s", lines, body)
	}
}

// TestChecksumsCoversISOsToo keeps the other half of the glob working,
// which is what `make release-all` relies on.
func TestChecksumsCoversISOsToo(t *testing.T) {
	preserveChecksums(t)

	const version = "v0.0.0-checksumtest-iso"
	stageArtifact(t, "lankeeper-"+version+"-linux-amd64.tar.gz")
	stageArtifact(t, "lankeeper-"+version+"-installer-amd64.iso")

	out, err := runChecksums(t, version)
	if err != nil {
		t.Fatalf("make checksums failed: %v\n%s", err, out)
	}

	raw, readErr := os.ReadFile(filepath.Join(repoRoot, "dist", "SHA256SUMS"))
	if readErr != nil {
		t.Fatalf("read SHA256SUMS: %v", readErr)
	}
	if !strings.Contains(string(raw), "installer-amd64.iso") {
		t.Errorf("the ISO is missing from SHA256SUMS:\n%s", raw)
	}
}

// TestChecksumsRefusesAnEmptyResult stops a release from publishing a
// checksum file that lists nothing, which reads as a release whose
// artifacts all verify.
func TestChecksumsRefusesAnEmptyResult(t *testing.T) {
	preserveChecksums(t)

	out, err := runChecksums(t, "v0.0.0-nothing-matches-this")
	if err == nil {
		t.Fatalf("make checksums succeeded with no artifacts:\n%s", out)
	}
	if !strings.Contains(out, "no release artifacts found") {
		t.Errorf("the failure does not say why:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, "dist", "SHA256SUMS")); statErr == nil {
		t.Error("an empty SHA256SUMS was left behind")
	}
}
