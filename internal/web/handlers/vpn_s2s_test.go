package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

// newVPNHandlerForTest constructs a VPNHandler whose VPNService is
// wired against a temp-file-backed config so persist() succeeds.
// No agent is configured; tests stick to handlers that do not
// invoke wg/wg-quick (token shape validation, error paths).
func newVPNHandlerForTest(t *testing.T) (*VPNHandler, *services.VPNService) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.System.SessionSecret = "deterministic-test-session-secret-32"
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "lan0", Device: "eth1", Role: "lan", Address: "10.10.10.1/24"},
	}
	cfg.VPN.Server.PublicKey = "fakeServerPubKey"
	cfg.VPN.Server.Address = "10.10.11.1/24"
	cfg.VPN.Server.ListenPort = 51820
	svc := services.NewVPNService(cfg)
	h := NewVPNHandler(&tmpl.Renderer{}, svc)
	return h, svc
}

func TestHandleS2SInviteValidation(t *testing.T) {
	h, _ := newVPNHandlerForTest(t)

	cases := []struct {
		label string
		form  url.Values
		want  int
	}{
		{
			label: "missing name",
			form:  url.Values{"endpoint": {"1.2.3.4:51820"}, "remoteSubnets": {"192.168.5.0/24"}},
			want:  http.StatusBadRequest,
		},
		{
			label: "bad name chars",
			form:  url.Values{"name": {"bad name!"}, "endpoint": {"1.2.3.4:51820"}, "remoteSubnets": {"192.168.5.0/24"}},
			want:  http.StatusBadRequest,
		},
		{
			label: "missing endpoint",
			form:  url.Values{"name": {"siteB"}, "remoteSubnets": {"192.168.5.0/24"}},
			want:  http.StatusBadRequest,
		},
		{
			label: "endpoint without port",
			form:  url.Values{"name": {"siteB"}, "endpoint": {"1.2.3.4"}, "remoteSubnets": {"192.168.5.0/24"}},
			want:  http.StatusBadRequest,
		},
		{
			label: "no remote subnet",
			form:  url.Values{"name": {"siteB"}, "endpoint": {"1.2.3.4:51820"}},
			want:  http.StatusBadRequest,
		},
		{
			label: "bad CIDR",
			form:  url.Values{"name": {"siteB"}, "endpoint": {"1.2.3.4:51820"}, "remoteSubnets": {"not-a-cidr"}},
			want:  http.StatusBadRequest,
		},
		{
			label: "subnet conflicts with local LAN",
			form:  url.Values{"name": {"siteB"}, "endpoint": {"1.2.3.4:51820"}, "remoteSubnets": {"10.10.10.0/24"}},
			want:  http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/vpn/s2s/invite",
				strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			h.HandleS2SInvite(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got status %d, want %d (body: %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestHandleS2SJoinRejectsBadToken(t *testing.T) {
	h, _ := newVPNHandlerForTest(t)
	form := url.Values{"token": {"obviously.notvalid"}}
	req := httptest.NewRequest(http.MethodPost, "/vpn/s2s/join", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleS2SJoin(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed token, got %d", rr.Code)
	}
}

func TestHandleS2SFinalizeValidation(t *testing.T) {
	h, _ := newVPNHandlerForTest(t)

	cases := []struct {
		label string
		form  url.Values
	}{
		{"missing peer name", url.Values{"ackToken": {"x.y"}}},
		{"bad peer name", url.Values{"peerName": {"bad name!"}, "ackToken": {"x.y"}}},
		{"missing ack token", url.Values{"peerName": {"siteB"}}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/vpn/s2s/finalize",
				strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			h.HandleS2SFinalize(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("got %d, want 400", rr.Code)
			}
		})
	}
}

func TestJSONStringEscapes(t *testing.T) {
	cases := map[string]string{
		`abc`:  `"abc"`,
		`a"b`:  `"a\"b"`,
		`a\b`:  `"a\\b"`,
		"a\nb": `"a\nb"`,
		"a\tb": `"a\tb"`,
		"\x01": `"\u0001"`,
	}
	for in, want := range cases {
		if got := jsonString(in); got != want {
			t.Errorf("jsonString(%q) = %q, want %q", in, got, want)
		}
	}
}
