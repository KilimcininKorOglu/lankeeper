package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempArchive drops a file the checksum path can hash. The content
// is arbitrary; only the digest matters.
func writeTempArchive(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return p
}

// TestVerifyChecksumRefusesReleaseWithoutChecksumAsset is the fix. The
// previous behaviour logged a line and returned nil, which ApplyUpdate
// could not tell apart from a real verification, so it went on to
// overwrite /usr/local/bin/lankeeper through the root agent.
func TestVerifyChecksumRefusesReleaseWithoutChecksumAsset(t *testing.T) {
	svc := &UpdateService{}
	archive := writeTempArchive(t, "lankeeper-v1.2.3-linux-amd64.tar.gz", "payload")

	err := svc.verifyChecksum(context.Background(), &UpdateInfo{ChecksumURL: ""}, archive)
	if err == nil {
		t.Fatal("a release with no checksum asset was accepted for install")
	}
	if !strings.Contains(err.Error(), "unverified") {
		t.Errorf("error does not explain the refusal: %v", err)
	}
}

// TestVerifyChecksumAcceptsMatchingDigest keeps the fix from being a
// blanket denial, and pins the SHA256SUMS line format the release
// target emits: "<hash>  <filename>".
func TestVerifyChecksumAcceptsMatchingDigest(t *testing.T) {
	const name = "lankeeper-v1.2.3-linux-amd64.tar.gz"
	// sha256 of "payload"
	const digest = "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5"

	archive := writeTempArchive(t, name, "payload")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(digest + "  " + name + "\n"))
	}))
	defer srv.Close()

	svc := &UpdateService{}
	if err := svc.verifyChecksum(context.Background(), &UpdateInfo{ChecksumURL: srv.URL}, archive); err != nil {
		t.Errorf("matching digest was rejected: %v", err)
	}
}

// TestVerifyChecksumRejectsAlteredArchive is the case the check exists
// for: the asset served does not match what the release recorded.
func TestVerifyChecksumRejectsAlteredArchive(t *testing.T) {
	const name = "lankeeper-v1.2.3-linux-amd64.tar.gz"
	const digest = "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5"

	archive := writeTempArchive(t, name, "tampered")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(digest + "  " + name + "\n"))
	}))
	defer srv.Close()

	svc := &UpdateService{}
	err := svc.verifyChecksum(context.Background(), &UpdateInfo{ChecksumURL: srv.URL}, archive)
	if err == nil {
		t.Fatal("archive whose digest does not match the release was accepted")
	}
}

// TestVerifyChecksumRejectsMissingEntry covers a checksum file that
// exists but does not list this architecture's archive.
func TestVerifyChecksumRejectsMissingEntry(t *testing.T) {
	archive := writeTempArchive(t, "lankeeper-v1.2.3-linux-arm64.tar.gz", "payload")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("deadbeef  lankeeper-v1.2.3-linux-amd64.tar.gz\n"))
	}))
	defer srv.Close()

	svc := &UpdateService{}
	err := svc.verifyChecksum(context.Background(), &UpdateInfo{ChecksumURL: srv.URL}, archive)
	if err == nil {
		t.Fatal("archive absent from the checksum file was accepted")
	}
	if !strings.Contains(err.Error(), "no checksum found") {
		t.Errorf("unexpected error: %v", err)
	}
}
