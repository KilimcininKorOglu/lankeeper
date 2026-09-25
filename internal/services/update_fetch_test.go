package services

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUpdateFetchesRefuseInternalAddresses is the regression test. The
// update path built bare http.Clients, so a download or checksum URL
// taken from the release metadata, or a redirect from it, could make
// this process connect to its own loopback services or the LAN behind
// it. Both fetches now go through the guarded clients, which refuse the
// loopback test server at dial time.
func TestUpdateFetchesRefuseInternalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "reached\n")
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	svc := &UpdateService{currentVersion: "v1.0.0"}
	err := svc.downloadFile(ctx, srv.URL, filepath.Join(t.TempDir(), "asset"), 0)
	if err == nil || !strings.Contains(err.Error(), "internal address") {
		t.Errorf("downloadFile reached a loopback server: %v", err)
	}
	if _, err := fetchChecksumFile(ctx, srv.URL); err == nil || !strings.Contains(err.Error(), "internal address") {
		t.Errorf("fetchChecksumFile reached a loopback server: %v", err)
	}
}

// TestUpdateBuildsNoBareHTTPClient covers fetchLatestRelease, whose URL
// is fixed to api.github.com and so offers no seam for a test server.
// A bare client anywhere in the file would skip the address guard.
func TestUpdateBuildsNoBareHTTPClient(t *testing.T) {
	raw, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatalf("read update.go: %v", err)
	}
	if strings.Contains(string(raw), "&http.Client{") {
		t.Error("update.go builds its own http.Client; use the guarded clients in safefetch.go")
	}
}
