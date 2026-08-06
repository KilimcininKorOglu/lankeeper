package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// endlessAssetServer streams past the cap without ever declaring a
// size. Bounded well above the limit rather than truly infinite so the
// test still terminates when the cap is missing, letting the assertion
// report the failure instead of a timeout.
func endlessAssetServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.CopyN(w, zeroReader{}, maxUpdateArchiveBytes+(8<<20))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDownloadRefusesAnOversizedDeclaredAsset is the cheap half: the
// API publishes the asset size and it was parsed but never compared
// against anything, so an oversized release was fetched in full before
// anyone noticed.
func TestDownloadRefusesAnOversizedDeclaredAsset(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	defer srv.Close()

	svc := &UpdateService{currentVersion: "v1.0.0"}
	dest := filepath.Join(t.TempDir(), "asset.tar.gz")

	err := svc.downloadFile(context.Background(), srv.URL, dest, maxUpdateArchiveBytes+1)
	if err == nil {
		t.Fatal("an asset declaring more than the limit was accepted")
	}
	if !errors.Is(err, errUpdateTooLarge) {
		t.Errorf("got %v, want a size-limit error", err)
	}
	if hits != 0 {
		t.Errorf("the server was contacted %d times; the declared size should settle it first", hits)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a destination file was created for a refused asset")
	}
}

// TestDownloadCapsAnUndeclaredStream is the regression test proper. A
// release that understates its size, or a response body that simply
// does not stop, was copied to /tmp with no ceiling at all. The router
// has one root filesystem, so filling it destabilises everything on it,
// and this write happens before verification completes.
func TestDownloadCapsAnUndeclaredStream(t *testing.T) {
	srv := endlessAssetServer(t)

	svc := &UpdateService{currentVersion: "v1.0.0"}
	dest := filepath.Join(t.TempDir(), "asset.tar.gz")

	err := svc.downloadFile(context.Background(), srv.URL, dest, 0)
	if err == nil {
		t.Fatal("an unbounded response body was accepted")
	}
	if !errors.Is(err, errUpdateTooLarge) {
		t.Errorf("got %v, want a size-limit error", err)
	}

	// The cap has to bound what reached the disk, not merely report a
	// problem after the fact.
	info, statErr := os.Stat(dest)
	if statErr != nil {
		t.Fatalf("stat destination: %v", statErr)
	}
	if info.Size() > maxUpdateArchiveBytes+1 {
		t.Errorf("wrote %d bytes to disk, cap is %d", info.Size(), int64(maxUpdateArchiveBytes))
	}
}

// TestDownloadRejectsASizeMismatch catches an asset whose body and
// published size describe different artifacts.
func TestDownloadRejectsASizeMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()

	svc := &UpdateService{currentVersion: "v1.0.0"}
	dest := filepath.Join(t.TempDir(), "asset.tar.gz")

	if err := svc.downloadFile(context.Background(), srv.URL, dest, 4096); err == nil {
		t.Fatal("a body that disagreed with the declared size was accepted")
	}
}

// TestDownloadAcceptsARealSizedAsset keeps the cap from rejecting the
// releases it exists to let through.
func TestDownloadAcceptsARealSizedAsset(t *testing.T) {
	payload := make([]byte, 3<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	svc := &UpdateService{currentVersion: "v1.0.0"}
	dest := filepath.Join(t.TempDir(), "asset.tar.gz")

	if err := svc.downloadFile(context.Background(), srv.URL, dest, int64(len(payload))); err != nil {
		t.Fatalf("a normal-sized asset was refused: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(len(payload)) {
		t.Errorf("wrote %d bytes, want %d", info.Size(), len(payload))
	}
}

// writeGzipTar builds an archive holding one entry called lankeeper,
// declaring declaredSize in its header while carrying actualSize bytes
// of content, so the two can be made to disagree. Declaring a huge size
// without writing it is what keeps this test cheap: extractBinary reads
// the header before it reads a byte of the body, so the shortfall the
// tar writer reports at close is expected and ignored.
func writeGzipTar(t *testing.T, declaredSize, actualSize int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "release.tar.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer func() { _ = f.Close() }()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "lankeeper",
		Mode:     0o755,
		Size:     declaredSize,
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	// A run of zeros compresses to almost nothing, which is exactly the
	// expansion ratio an unbounded extraction cannot defend against.
	if _, err := io.CopyN(tw, zeroReader{}, actualSize); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if declaredSize == actualSize {
		if err := tw.Close(); err != nil {
			t.Fatalf("close tar: %v", err)
		}
	} else {
		_ = tw.Close()
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return p
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// TestExtractRefusesAnOversizedEntry covers the declared-size check on
// the archive header.
func TestExtractRefusesAnOversizedEntry(t *testing.T) {
	// Declares more than the cap while carrying almost nothing, so the
	// archive on disk stays small and the check is reached before any
	// body is read.
	archive := writeGzipTar(t, maxUpdateBinaryBytes+1, 512)

	svc := &UpdateService{}
	dest := filepath.Join(t.TempDir(), "lankeeper-new")

	err := svc.extractBinary(archive, dest)
	if err == nil {
		t.Fatal("an entry declaring more than the limit was extracted")
	}
	if !errors.Is(err, errUpdateTooLarge) {
		t.Errorf("got %v, want a size-limit error", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a partial binary was left behind after a refused extraction")
	}
}

// TestExtractAcceptsARealBinary keeps the extraction cap from breaking
// the normal path: the shipped binaries are around 12 MB.
func TestExtractAcceptsARealBinary(t *testing.T) {
	const size = 12 << 20
	archive := writeGzipTar(t, size, size)

	svc := &UpdateService{}
	dest := filepath.Join(t.TempDir(), "lankeeper-new")

	if err := svc.extractBinary(archive, dest); err != nil {
		t.Fatalf("a normal-sized binary was refused: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != size {
		t.Errorf("extracted %d bytes, want %d", info.Size(), size)
	}
}

// TestCopyCappedAcceptsExactlyTheLimit guards the boundary, so a
// payload sitting precisely on the cap is not rejected by an off-by-one.
func TestCopyCappedAcceptsExactlyTheLimit(t *testing.T) {
	var sink countingWriter
	n, err := copyCapped(&sink, io.LimitReader(zeroReader{}, 1024), 1024)
	if err != nil {
		t.Fatalf("a payload exactly at the limit was refused: %v", err)
	}
	if n != 1024 {
		t.Errorf("copied %d bytes, want 1024", n)
	}

	if _, err := copyCapped(&sink, io.LimitReader(zeroReader{}, 1025), 1024); !errors.Is(err, errUpdateTooLarge) {
		t.Errorf("one byte over the limit gave %v, want a size-limit error", err)
	}
}

type countingWriter int64

func (c *countingWriter) Write(p []byte) (int, error) {
	*c += countingWriter(len(p))
	return len(p), nil
}
