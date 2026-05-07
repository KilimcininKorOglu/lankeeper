package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func TestMetricsHandlerContentTypeAndBody(t *testing.T) {
	cfg := &config.Config{}
	// Pass nil-safe references; the snapshot composer guards
	// every contributor and degrades to a minimal output. We
	// only need to confirm the wire format here.
	mSvc := services.NewMetricsService(cfg, nil, nil, nil, nil, nil, nil, nil)
	h := NewMetricsHandler(mSvc)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.HandleMetrics(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if got := res.StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if got := res.Header.Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"lankeeper_uptime_seconds",
		"lankeeper_cpu_percent",
		"# TYPE lankeeper_dhcp_active_leases gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}
