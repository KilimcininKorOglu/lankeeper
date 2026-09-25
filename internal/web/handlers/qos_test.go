package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/web/handlers"
)

// TestQoSApplyRejectsWithoutTouchingTheConfig covers a rejected form.
// The handler used to assign each field as soon as that field passed, so
// a valid profile followed by an invalid bandwidth answered 400 while the
// live config already held the new profile, and the next successful save
// wrote the rejected submission to disk.
func TestQoSApplyRejectsWithoutTouchingTheConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.QoS.Profile = "fq_codel"
	cfg.QoS.UploadKbps = 1000
	cfg.QoS.CongestionControl = "cubic"
	before := cfg.QoS

	// The QoS service is never reached on a rejected form, so it is nil.
	h := handlers.NewQoSHandler(nil, nil, cfg)

	form := url.Values{
		"profile":           {"cake"},
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

// qosRecordingAgent answers every privileged call with success and
// keeps the sysctl arguments it was given.
type qosRecordingAgent struct{ sysctl []string }

func (a *qosRecordingAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if method == "exec.run" && p.Cmd == "sysctl" {
		a.sysctl = append(a.sysctl, strings.Join(p.Args, " "))
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

// TestQoSApplyAcceptsTheFormTheUISends is the regression test. The
// handler accepted only the profiles default, gaming, streaming and
// voip, while the page offers cake, fq_codel and none, the only values
// the service can apply. Every submit from the page was answered 400.
func TestQoSApplyAcceptsTheFormTheUISends(t *testing.T) {
	agent := &qosRecordingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := config.DefaultConfig()
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.Interfaces = []config.InterfaceConfig{{ID: "wan0", Device: "eth0", Role: "wan"}}
	h := handlers.NewQoSHandler(nil, services.NewQoSService(cfg), cfg)

	for _, profile := range []string{"cake", "fq_codel", "none"} {
		form := url.Values{
			"profile":           {profile},
			"uploadKbps":        {"20000"},
			"downloadKbps":      {"100000"},
			"congestionControl": {"cubic"},
			"enabled":           {"true"},
		}
		req := httptest.NewRequest(http.MethodPost, "/qos/apply", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.HandleApply(rec, req)

		if rec.Code >= 300 && rec.Code != http.StatusSeeOther {
			t.Errorf("profile %s: status %d, body %q", profile, rec.Code, rec.Body.String())
		}
		if cfg.QoS.Profile != profile {
			t.Errorf("profile %s was not stored: %q", profile, cfg.QoS.Profile)
		}
	}
}

// TestQoSApplyRefusesAQdiscAsCongestionControl keeps "cake" out of the
// congestion control field. It is a qdisc, not a TCP algorithm, so the
// service would hand the kernel net.ipv4.tcp_congestion_control=cake.
func TestQoSApplyRefusesAQdiscAsCongestionControl(t *testing.T) {
	cfg := config.DefaultConfig()
	before := cfg.QoS
	h := handlers.NewQoSHandler(nil, nil, cfg)

	form := url.Values{"profile": {"cake"}, "congestionControl": {"cake"}}
	req := httptest.NewRequest(http.MethodPost, "/qos/apply", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.HandleApply(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "error.invalidCongestionControl") {
		t.Fatalf("status = %d body %q, want 400 error.invalidCongestionControl", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(cfg.QoS, before) {
		t.Errorf("a rejected form changed the live config: got %+v", cfg.QoS)
	}
}
