package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// sharedDirAgent models the root agent across the systemd sandbox: it
// runs cp on the real filesystem, but only sees files under the visible
// directories, which stand for the data directory and the install
// location. A path in the web process's private /tmp does not exist
// for it.
type sharedDirAgent struct {
	mu      sync.Mutex
	visible []string
	cpSrc   []string
}

func (a *sharedDirAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return []byte(`{}`), nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Cmd == "cp" {
		return a.copyFile(p.Args[1], p.Args[2])
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func (a *sharedDirAgent) copyFile(src, dst string) (json.RawMessage, error) {
	a.mu.Lock()
	a.cpSrc = append(a.cpSrc, src)
	a.mu.Unlock()
	if !a.sees(src) {
		return nil, fmt.Errorf("cp: cannot stat '%s': No such file or directory", src)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		return nil, err
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

// sees reports whether path lies under a visible directory.
func (a *sharedDirAgent) sees(path string) bool {
	for _, dir := range a.visible {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// releaseArchive packs a stand-in binary that reports version.
func releaseArchive(t *testing.T, version string) []byte {
	t.Helper()
	script := []byte("#!/bin/sh\necho lankeeper " + version + "\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "lankeeper", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(script); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// releaseServer serves the archive and a SHA256SUMS file naming it.
func releaseServer(t *testing.T, assetName string, archive []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + assetName + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset":
			_, _ = w.Write(archive)
		case "/sums":
			_, _ = io.WriteString(w, sums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestApplyUpdateStagesTheBinaryWhereTheAgentCanReadIt is the regression
// test. The new binary was extracted to /tmp/lankeeper-new by the web
// process and copied into place by the agent, but both units run with
// PrivateTmp, so the agent's cp looked for it in a different /tmp and
// every update failed at the install step.
func TestApplyUpdateStagesTheBinaryWhereTheAgentCanReadIt(t *testing.T) {
	publicLoopbackClient(t)
	dataDir := t.TempDir()
	t.Setenv("LANKEEPER_UPDATE_STATE", filepath.Join(dataDir, "update-state.json"))

	binDir := t.TempDir()
	agent := &sharedDirAgent{visible: []string{dataDir, binDir}}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewUpdateService("v1.0.0", "", "", nil)
	svc.binaryPath = filepath.Join(binDir, "lankeeper")
	if err := os.WriteFile(svc.binaryPath, []byte("#!/bin/sh\necho lankeeper v1.0.0\n"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	const asset = "lankeeper-v9.9.9-linux-arm64.tar.gz"
	archive := releaseArchive(t, "v9.9.9")
	srv := releaseServer(t, asset, archive)

	err := svc.ApplyUpdate(context.Background(), &UpdateInfo{
		LatestVersion: "v9.9.9",
		DownloadURL:   srv.URL + "/asset",
		ChecksumURL:   srv.URL + "/sums",
		AssetName:     asset,
		AssetSize:     int64(len(archive)),
	})
	t.Cleanup(func() { _ = svc.ConfirmUpdate(context.Background()) })
	if err != nil {
		t.Fatalf("apply: %v (agent cp sources: %q)", err, agent.cpSrc)
	}
	installed, err := os.ReadFile(svc.binaryPath)
	if err != nil || !strings.Contains(string(installed), "v9.9.9") {
		t.Errorf("the new binary was not installed: %q, %v", installed, err)
	}
}
