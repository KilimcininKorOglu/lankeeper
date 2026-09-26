package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// A router that restarts more often than the renewal interval must still
// check: the loop acts once when it starts, not only on its first tick.
func TestRenewalChecksWhenTheLoopStarts(t *testing.T) {
	tlsSvc, cfg, dir := newTLSTestService(t)
	t.Setenv("LANKEEPER_ACME_KEY", filepath.Join(dir, "acme-account.key"))
	svc := services.NewACMEServiceInDir(cfg, tlsSvc, dir, "https://acme.invalid/directory", "https://cf.invalid")
	if _, err := tlsSvc.Regenerate(context.Background(), "hermes.example", nil, 10); err != nil {
		t.Fatalf("seed certificate: %v", err)
	}
	cfg.System.TLS.Mode = "acme"
	cfg.System.TLS.ACME.Enabled = true
	cfg.System.TLS.ACME.Domain = "hermes.example"
	cfg.System.TLS.ACME.Email = "ops@example.com"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { svc.StartRenewal(ctx); close(done) }()

	// The account key is minted before the first request to the CA, so
	// its presence shows renewal was attempted.
	for ctx.Err() == nil {
		if _, err := os.Stat(filepath.Join(dir, "acme-account.key")); err == nil {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	<-done
	t.Fatal("starting the renewal loop did not check the certificate")
}
