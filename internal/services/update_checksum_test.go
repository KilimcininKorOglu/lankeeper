package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

// signWithTestKey swaps the release key for a fresh one for the rest of
// the test and returns the base64 signature of sums under it.
func signWithTestKey(t *testing.T, sums string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	prev := releaseSigningKey
	releaseSigningKey = pub
	t.Cleanup(func() { releaseSigningKey = prev })
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums)))
}

// sumsServer serves a SHA256SUMS body at /sums and sig at /sig.
func sumsServer(t *testing.T, sums, sig string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sums":
			_, _ = w.Write([]byte(sums))
		case "/sig":
			_, _ = w.Write([]byte(sig))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// releaseInfo describes release v1.2.3 with its assets on srv.
func releaseInfo(srv *httptest.Server, asset string) *UpdateInfo {
	return &UpdateInfo{
		LatestVersion: "v1.2.3",
		AssetName:     asset,
		ChecksumURL:   srv.URL + "/sums",
		SignatureURL:  srv.URL + "/sig",
	}
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
	publicLoopbackClient(t)
	const name = "lankeeper-v1.2.3-linux-amd64.tar.gz"
	// sha256 of "payload"
	const sums = "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5  " + name + "\n"

	archive := writeTempArchive(t, name, "payload")
	srv := sumsServer(t, sums, signWithTestKey(t, sums))

	svc := &UpdateService{}
	if err := svc.verifyChecksum(context.Background(), releaseInfo(srv, name), archive); err != nil {
		t.Errorf("matching digest was rejected: %v", err)
	}
}

// TestVerifyChecksumRejectsAlteredArchive is the case the check exists
// for: the asset served does not match what the release recorded.
func TestVerifyChecksumRejectsAlteredArchive(t *testing.T) {
	publicLoopbackClient(t)
	const name = "lankeeper-v1.2.3-linux-amd64.tar.gz"
	const sums = "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5  " + name + "\n"

	archive := writeTempArchive(t, name, "tampered")
	srv := sumsServer(t, sums, signWithTestKey(t, sums))

	svc := &UpdateService{}
	err := svc.verifyChecksum(context.Background(), releaseInfo(srv, name), archive)
	if err == nil {
		t.Fatal("archive whose digest does not match the release was accepted")
	}
	if !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Errorf("refused for another reason: %v", err)
	}
}

// TestVerifyChecksumRejectsMissingEntry covers a checksum file that
// exists but does not list this architecture's archive.
func TestVerifyChecksumRejectsMissingEntry(t *testing.T) {
	publicLoopbackClient(t)
	const name = "lankeeper-v1.2.3-linux-arm64.tar.gz"
	const sums = "deadbeef  lankeeper-v1.2.3-linux-amd64.tar.gz\n"
	archive := writeTempArchive(t, name, "payload")
	srv := sumsServer(t, sums, signWithTestKey(t, sums))

	svc := &UpdateService{}
	err := svc.verifyChecksum(context.Background(), releaseInfo(srv, name), archive)
	if err == nil {
		t.Fatal("archive absent from the checksum file was accepted")
	}
	if !strings.Contains(err.Error(), "no checksum found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// SHA256SUMS sits in the same editable release as the archive, so anyone
// able to replace one can replace both. Only a signature under the key
// compiled into the binary ties them to the maintainer, and a release
// without one, or with one from another key, is refused.
func TestVerifyChecksumRequiresTheReleaseSignature(t *testing.T) {
	publicLoopbackClient(t)
	const name = "lankeeper-v1.2.3-linux-amd64.tar.gz"
	const sums = "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5  " + name + "\n"
	archive := writeTempArchive(t, name, "payload")
	signWithTestKey(t, sums)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	forged := base64.StdEncoding.EncodeToString(ed25519.Sign(otherKey, []byte(sums)))

	for label, sig := range map[string]string{"foreign key": forged, "garbage": "not a signature"} {
		srv := sumsServer(t, sums, sig)
		if err := (&UpdateService{}).verifyChecksum(context.Background(), releaseInfo(srv, name), archive); err == nil {
			t.Errorf("%s: an unverified SHA256SUMS was accepted", label)
		}
	}

	srv := sumsServer(t, sums, "")
	info := releaseInfo(srv, name)
	info.SignatureURL = ""
	if err := (&UpdateService{}).verifyChecksum(context.Background(), info, archive); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Errorf("a release without SHA256SUMS.sig: err = %v", err)
	}
}

// A signed SHA256SUMS from an older release, uploaded with its archive
// under a newer tag, would otherwise verify and downgrade the router.
func TestVerifyChecksumRefusesAnAssetFromAnotherRelease(t *testing.T) {
	publicLoopbackClient(t)
	const name = "lankeeper-v1.0.0-linux-amd64.tar.gz"
	const sums = "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5  " + name + "\n"
	archive := writeTempArchive(t, name, "payload")
	srv := sumsServer(t, sums, signWithTestKey(t, sums))

	if err := (&UpdateService{}).verifyChecksum(context.Background(), releaseInfo(srv, name), archive); err == nil {
		t.Error("an archive built for v1.0.0 was accepted as v1.2.3")
	}
}
