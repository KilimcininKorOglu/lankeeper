package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

func queryLogService(t *testing.T, maxSize, retention string) (*DNSService, string) {
	t.Helper()
	netutil.SetAgentClient(nil)
	cfg := config.DefaultConfig()
	path := filepath.Join(t.TempDir(), "queries.log")
	cfg.DNS.QueryLog = config.QueryLogConfig{Enabled: true, LogPath: path, MaxSize: maxSize, Retention: retention}
	return NewDNSService(cfg), path
}

// Every LAN query lands in this file, so without a bound it fills /var/log.
func TestRotateQueryLogKeepsTheFileWithinMaxSize(t *testing.T) {
	svc, path := queryLogService(t, "1K", "7d")
	if err := os.WriteFile(path, []byte(strings.Repeat("q\n", 1024)), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := svc.rotateQueryLog(context.Background()); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("live log not emptied: %v size=%v", err, info)
	}
	if prev, err := os.ReadFile(path + ".1"); err != nil || len(prev) != 2048 {
		t.Fatalf("previous generation not kept: %v len=%d", err, len(prev))
	}
}

func TestRotateQueryLogLeavesASmallFileAlone(t *testing.T) {
	svc, path := queryLogService(t, "1M", "7d")
	if err := os.WriteFile(path, []byte("q\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := svc.rotateQueryLog(context.Background()); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("a file under the limit was rotated")
	}
}

func TestRotateQueryLogExpiresTheOldGeneration(t *testing.T) {
	svc, path := queryLogService(t, "1M", "1d")
	old := path + ".1"
	if err := os.WriteFile(old, []byte("q\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := svc.rotateQueryLog(context.Background()); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("a generation past its retention was kept")
	}
}

func TestParseQueryLogLimits(t *testing.T) {
	if n, err := parseByteSize("100M"); err != nil || n != 100<<20 {
		t.Errorf("100M = %d, %v", n, err)
	}
	if d, err := parseRetention("7d"); err != nil || d != 7*24*time.Hour {
		t.Errorf("7d = %v, %v", d, err)
	}
	for _, bad := range []string{"", "-1M", "x"} {
		if _, err := parseByteSize(bad); err == nil {
			t.Errorf("parseByteSize(%q) accepted", bad)
		}
	}
}
