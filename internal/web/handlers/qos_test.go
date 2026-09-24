package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/web/handlers"
)

// TestQoSApplyRejectsWithoutTouchingTheConfig covers a rejected form.
// The handler used to assign each field as soon as that field passed, so
// a valid profile followed by an invalid bandwidth answered 400 while the
// live config already held the new profile, and the next successful save
// wrote the rejected submission to disk.
func TestQoSApplyRejectsWithoutTouchingTheConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.QoS.Profile = "default"
	cfg.QoS.UploadKbps = 1000
	cfg.QoS.CongestionControl = "cubic"
	before := cfg.QoS

	// The QoS service is never reached on a rejected form, so it is nil.
	h := handlers.NewQoSHandler(nil, nil, cfg)

	form := url.Values{
		"profile":           {"gaming"},
		"uploadKbps":        {"2000"},
		"downloadKbps":      {"-1"},
		"congestionControl": {"bbr"},
		"enabled":           {"on"},
	}
	req := httptest.NewRequest(http.MethodPost, "/qos/apply", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.HandleApply(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !reflect.DeepEqual(cfg.QoS, before) {
		t.Errorf("a rejected form changed the live config: got %+v, want %+v", cfg.QoS, before)
	}
}
