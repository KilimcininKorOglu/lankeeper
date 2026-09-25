package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
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

// vlanIPAgent answers every privileged call with success and keeps the
// arguments of each ip command.
type vlanIPAgent struct{ ip []string }

func (a *vlanIPAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
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
	if method == "exec.run" && p.Cmd == "ip" {
		a.ip = append(a.ip, strings.Join(p.Args, " "))
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

// unsavableVLANConfig returns a config whose file lives in a directory
// that does not exist, so every SaveToFile fails.
func unsavableVLANConfig(t *testing.T) (*config.Config, *vlanIPAgent) {
	t.Helper()
	agent := &vlanIPAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := vlanHandlerConfig(t)
	cfg.SetFilePath(filepath.Join(t.TempDir(), "missing", "router.yaml"))
	return cfg, agent
}

// TestVLANAddKeepsNothingWhenTheSaveFails is the regression test. The
// VLAN was appended before the save, and a failed save left it in the
// running config, so the next successful save of any page wrote an entry
// the operator had been told was not stored.
func TestVLANAddKeepsNothingWhenTheSaveFails(t *testing.T) {
	cfg, agent := unsavableVLANConfig(t)
	h := handlers.NewVLANHandler(nil, services.NewNetworkService(cfg), cfg)

	rec := postVLAN(h, url.Values{
		"id": {"guest"}, "parent": {"lan"}, "vid": {"20"}, "label": {"Guest"},
		"role": {"lan"}, "type": {"static"}, "address": {"10.10.20.1/24"},
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(cfg.VLANs) != 0 {
		t.Errorf("an unsaved VLAN stayed in the running config: %+v", cfg.VLANs)
	}
	if len(agent.ip) != 0 {
		t.Errorf("an unsaved VLAN was created: %q", agent.ip)
	}
}

// TestVLANDeleteKeepsTheEntryWhenTheSaveFails is the regression test.
// The device was removed and the entry dropped before the save, so a
// failed save left a running config without the VLAN and a file on disk
// that still carried it, and the device was already gone either way.
func TestVLANDeleteKeepsTheEntryWhenTheSaveFails(t *testing.T) {
	cfg, agent := unsavableVLANConfig(t)
	vlan := config.VLANConfig{ID: "guest", Parent: "lan", VID: 20, Label: "Guest", Role: "lan", Type: "static"}
	cfg.VLANs = []config.VLANConfig{vlan}
	h := handlers.NewVLANHandler(nil, services.NewNetworkService(cfg), cfg)

	req := httptest.NewRequest(http.MethodPost, "/network/vlan/guest/delete", nil)
	req.SetPathValue("id", "guest")
	rec := httptest.NewRecorder()
	h.HandleDelete(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(cfg.VLANs) != 1 || cfg.VLANs[0].ID != vlan.ID {
		t.Errorf("the running config lost the VLAN: %+v", cfg.VLANs)
	}
	if len(agent.ip) != 0 {
		t.Errorf("the device was removed although the entry stayed: %q", agent.ip)
	}
}
