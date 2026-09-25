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

func vlanHandlerConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{{ID: "lan", Device: "eth1", Role: "lan", Type: "static"}}
	cfg.VLANs = nil
	return cfg
}

func postVLAN(h *handlers.VLANHandler, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/network/vlan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.HandleAdd(rec, req)
	return rec
}

// TestVLANAddRefusesAnEntryTheConfigCannotCarry is the regression test
// for the handler path: a VLAN with no parent was stored, and the next
// config load, which validates, would then refuse to start the service.
func TestVLANAddRefusesAnEntryTheConfigCannotCarry(t *testing.T) {
	cfg := vlanHandlerConfig(t)
	h := handlers.NewVLANHandler(nil, services.NewNetworkService(cfg), cfg)

	rec := postVLAN(h, url.Values{"id": {"guest"}, "vid": {"20"}, "role": {"lan"}, "type": {"static"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(cfg.VLANs) != 0 {
		t.Errorf("a refused VLAN was stored: %+v", cfg.VLANs)
	}
}
