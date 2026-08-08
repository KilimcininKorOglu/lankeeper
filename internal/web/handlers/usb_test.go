package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/web/handlers"
)

func usbHandler(t *testing.T) (*handlers.NetworkHandler, *config.Config) {
	t.Helper()

	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	// An interface name that cannot exist, so Activate takes the
	// not-connected branch without depending on the host's NICs.
	cfg.USBTether.Interface = "lankeeper-test-absent0"

	h := handlers.NewNetworkHandler(
		nil,
		services.NewNetworkService(cfg),
		services.NewPPPoEService(cfg),
		services.NewUSBTetheringService(cfg),
		services.NewHealthCheckService(cfg),
	)
	return h, cfg
}

// A missing cable is the operator's problem to fix, not a fault in the
// router. Answering 500 tells them something is broken and hides the
// one thing they can act on, so the not-connected case has to be
// separated from a genuine failure.
func TestUSBActivateWithoutPhoneIsAClientError(t *testing.T) {
	h, _ := usbHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/network/usb/activate", nil)
	h.HandleUSBActivate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d so the operator sees a correctable condition",
			rec.Code, http.StatusBadRequest)
	}
	if strings.TrimSpace(rec.Body.String()) == "" {
		t.Error("the refusal carried no message, so the page shows a bare failure")
	}
}

// Enable and disable write policy only. Tearing down a live session on
// disable would drop the connection an operator may be managing the
// router over.
func TestUSBEnableDisableWriteConfigOnly(t *testing.T) {
	h, cfg := usbHandler(t)

	rec := httptest.NewRecorder()
	h.HandleUSBEnable(rec, httptest.NewRequest(http.MethodPost, "/network/usb/enable", nil))
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d", rec.Code)
	}
	if !cfg.USBTether.Enabled {
		t.Error("enable did not reach the config")
	}

	rec = httptest.NewRecorder()
	h.HandleUSBDisable(rec, httptest.NewRequest(http.MethodPost, "/network/usb/disable", nil))
	if cfg.USBTether.Enabled {
		t.Error("disable did not reach the config")
	}
}

// An unchecked checkbox submits no field at all, so the handler has to
// read its absence as "off". Reading only the present case would make
// the control one-way: switchable on, never off.
func TestUSBAutoFailoverAbsentFieldMeansOff(t *testing.T) {
	h, cfg := usbHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/network/usb/auto-failover",
		strings.NewReader("enabled=true"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.HandleUSBAutoFailover(rec, req)
	if !cfg.USBTether.AutoFailover {
		t.Fatal("checked box did not enable auto failover")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/network/usb/auto-failover", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.HandleUSBAutoFailover(rec, req)
	if cfg.USBTether.AutoFailover {
		t.Error("an empty submission left auto failover on, so the box cannot be cleared")
	}
}
