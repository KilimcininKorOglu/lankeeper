package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/web/handlers"
)

// TestHandleSubnetMapInputValidation exercises the per-request
// validation in HandleSubnetMap that runs BEFORE the service-layer
// validation (and before ApplyConfig). Bad JSON, an empty list, a
// list whose first entry is not "lan", and duplicate entries must
// all fail with HTTP 400 without touching IPv6Service.SetSubnetMap.
//
// The valid-path "service accepts and persists" contract is covered
// by service-level tests (see ipv6_test.go::TestIPv6ValidateSubnetMap
// and TestIPv6AnnouncedReflectsSubnetMap) which avoid the
// fakeAgent + SetFilePath wiring needed to round-trip ApplyConfig.
func TestHandleSubnetMapInputValidation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan", Device: "eth1", Role: "lan"},
	}
	svc := services.NewIPv6Service(cfg)
	h := handlers.NewIPv6Handler(nil, cfg, svc, nil, nil)

	cases := []struct {
		name string
		body string
	}{
		{"bad json", `not json`},
		{"empty array", `[]`},
		{"first not lan", `["guest","lan"]`},
		{"duplicate entry", `["lan","guest","guest"]`},
		{"empty entry", `["lan",""]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/ipv6/subnet-map", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.HandleSubnetMap(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %q, got %d (body=%q)", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}
