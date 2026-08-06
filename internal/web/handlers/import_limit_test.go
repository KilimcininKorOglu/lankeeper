package handlers

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// importRequest builds a multipart upload of the requested size,
// streaming the payload so the test itself does not hold it in memory.
func importRequest(t *testing.T, size int) *http.Request {
	t.Helper()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer func() { _ = pw.Close() }()
		part, err := mw.CreateFormFile("backup", "backup.tar.gz")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for written := 0; written < size; {
			n := len(chunk)
			if remaining := size - written; remaining < n {
				n = remaining
			}
			if _, err := part.Write(chunk[:n]); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			written += n
		}
		if err := mw.WriteField("passphrase", ""); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = mw.Close()
	}()

	req := httptest.NewRequest(http.MethodPost, "/settings/import", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func newImportHandler(t *testing.T) *SystemHandler {
	t.Helper()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	return NewSystemHandler(nil, cfg, nil, nil, services.NewBackupService(t.TempDir()), nil)
}

// countTempFiles reports how many import scratch files exist, which is
// how an oversized upload used to show up on disk.
func countTempFiles(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "lankeeper-import-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return len(matches)
}

// TestImportRejectsAnOversizedUpload is the regression test. The handler
// went straight to FormFile and copied the body into the temp directory
// in full before anything inspected it, so one admin session could fill
// the router's disk, or its RAM where TMPDIR is tmpfs-backed.
func TestImportRejectsAnOversizedUpload(t *testing.T) {
	h := newImportHandler(t)
	before := countTempFiles(t)

	rec := httptest.NewRecorder()
	h.HandleImport(rec, importRequest(t, maxBackupUploadBytes+(1<<20)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if after := countTempFiles(t); after > before {
		t.Errorf("%d import temp file(s) were left behind", after-before)
	}
}

// TestImportAcceptsAnUploadUnderTheCap keeps the limit from rejecting a
// real backup. The archive is not a valid one, so the import itself
// fails, but it must fail on its contents rather than on its size.
func TestImportAcceptsAnUploadUnderTheCap(t *testing.T) {
	h := newImportHandler(t)

	rec := httptest.NewRecorder()
	h.HandleImport(rec, importRequest(t, 1<<20))

	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Error("a 1 MiB upload was rejected as too large")
	}
	if rec.Code == http.StatusBadRequest {
		t.Errorf("the upload was not parsed at all: %s", rec.Body.String())
	}
}

// TestImportRejectsAMissingFile keeps the original contract: a request
// without the field is a client error, not a size error.
func TestImportRejectsAMissingFile(t *testing.T) {
	h := newImportHandler(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("passphrase", "x"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/settings/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rec := httptest.NewRecorder()
	h.HandleImport(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
