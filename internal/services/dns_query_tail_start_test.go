package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// Serve is the only place the tail can start on the shutdown context;
// without it the query log, top lists and blocked counter stay empty.
func TestServeStartsTheQueryLogTail(t *testing.T) {
	src, err := os.ReadFile("../web/server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "s.dnsSvc.StartQueryLogTail(ctx, &bg)") {
		t.Fatal("Serve does not start the DNS query log tail")
	}
}

func TestQueryLogTailStopsWithItsContext(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DNS.QueryLog.Enabled = true
	cfg.DNS.QueryLog.LogPath = filepath.Join(t.TempDir(), "unbound.log")
	svc := NewDNSService(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	svc.StartQueryLogTail(ctx, &wg)
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("query log tail did not stop with its context")
	}
}
