package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// appendQueryLog appends lines to the query log file.
func appendQueryLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("append log: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
}

// waitForQueries polls until the buffer holds n entries or the deadline
// passes, and returns what it holds.
func waitForQueries(svc *DNSService, n int) []QueryLogEntry {
	deadline := time.Now().Add(4 * time.Second)
	for {
		got := svc.GetRecentQueries(100, 0)
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestQueryLogTailKeepsLinesWrittenBetweenPolls pins that the tail reads
// every line unbound appends after it starts, and none written before.
// The tail used to reopen the file and seek to its end on every poll, so
// each line written while it slept between polls was skipped.
func TestQueryLogTailKeepsLinesWrittenBetweenPolls(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "queries.log")
	if err := os.WriteFile(logPath, []byte("[1700000000] unbound[1:0] info: 10.10.10.9 old.example. A IN\n"), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	cfg := &config.Config{}
	cfg.DNS.QueryLog.LogPath = logPath
	svc := NewDNSService(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.tailQueryLog(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Let the first poll pass, so the lines below land while the tail
	// waits for the next one.
	time.Sleep(300 * time.Millisecond)
	appendQueryLog(t, logPath,
		"[1700000001] unbound[1:0] info: 10.10.10.5 ads.example. A IN REFUSED",
		"[1700000002] unbound[1:0] info: 10.10.10.6 www.example. AAAA IN")

	got := waitForQueries(svc, 2)
	if len(got) != 2 {
		t.Fatalf("buffer holds %d entries, want 2: %+v", len(got), got)
	}
	domains := map[string]bool{got[0].Domain: true, got[1].Domain: true}
	if !domains["ads.example"] || !domains["www.example"] {
		t.Errorf("unexpected entries: %+v", got)
	}

	// A line written without its newline yet is read once it completes.
	writeSplitLine(t, logPath, "[1700000003] unbound[1:0] info: 10.10.10.7 half", ".example. A IN\n", 1500*time.Millisecond)
	// GetRecentQueries returns the newest entry first.
	got = waitForQueries(svc, 3)
	if len(got) != 3 || got[0].Domain != "half.example" {
		t.Errorf("after the split line the buffer holds %+v", got)
	}
}

// writeSplitLine appends head, waits across at least one poll, then
// appends tail.
func writeSplitLine(t *testing.T, path, head, tail string, pause time.Duration) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close log: %v", err)
		}
	}()
	if _, err := f.WriteString(head); err != nil {
		t.Fatalf("write line head: %v", err)
	}
	time.Sleep(pause)
	if _, err := f.WriteString(tail); err != nil {
		t.Fatalf("write line tail: %v", err)
	}
}
