package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/web/handlers"
)

func newDNSHandler(t *testing.T) (*handlers.DNSHandler, *config.Config) {
	t.Helper()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	dns := services.NewDNSService(cfg)
	doh := services.NewDoHService(cfg)
	return handlers.NewDNSHandler(nil, dns, doh), cfg
}

func TestSaveDoTHandlerPlainMode(t *testing.T) {
	h, cfg := newDNSHandler(t)
	form := url.Values{
		"encryption_mode": {"plain"},
	}
	req := httptest.NewRequest("POST", "/dns/dot", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleSaveDoT(rr, req)
	if cfg.DNS.EnableDoT || cfg.DNS.EnableDoH {
		t.Errorf("plain mode left flags set: DoT=%v DoH=%v", cfg.DNS.EnableDoT, cfg.DNS.EnableDoH)
	}
}

func TestSaveDoTHandlerDoHCatalogue(t *testing.T) {
	h, cfg := newDNSHandler(t)
	form := url.Values{
		"encryption_mode": {"doh"},
		"doh_upstream":    {"cloudflare"},
	}
	req := httptest.NewRequest("POST", "/dns/dot", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleSaveDoT(rr, req)
	if !cfg.DNS.EnableDoH {
		t.Errorf("DoH not enabled, status=%d", rr.Code)
	}
	if cfg.DNS.DoHUpstream != "cloudflare" {
		t.Errorf("upstream = %q", cfg.DNS.DoHUpstream)
	}
}

func TestSaveDoTHandlerDoHRejectsBad(t *testing.T) {
	h, _ := newDNSHandler(t)
	form := url.Values{
		"encryption_mode": {"doh"},
		"doh_upstream":    {"https://192.168.1.1/dns-query"}, // internal IP
	}
	req := httptest.NewRequest("POST", "/dns/dot", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleSaveDoT(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (internal IP rejected)", rr.Code)
	}
}

func TestSaveDoTHandlerUnknownMode(t *testing.T) {
	h, _ := newDNSHandler(t)
	form := url.Values{"encryption_mode": {"odoh"}}
	req := httptest.NewRequest("POST", "/dns/dot", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleSaveDoT(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestProbeDoHRequiresUpstream(t *testing.T) {
	h, _ := newDNSHandler(t)
	form := url.Values{}
	req := httptest.NewRequest("POST", "/dns/doh/probe", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleProbeDoH(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestProbeDoHCataloguePickReturnsError(t *testing.T) {
	h, _ := newDNSHandler(t)
	form := url.Values{"doh_upstream": {"cloudflare"}}
	req := httptest.NewRequest("POST", "/dns/doh/probe", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleProbeDoH(rr, req)
	// 200 with FAIL badge body - probe HTML response, not HTTP error.
	if !strings.Contains(rr.Body.String(), "FAIL") {
		t.Errorf("body should contain FAIL badge: %s", rr.Body.String())
	}
}
