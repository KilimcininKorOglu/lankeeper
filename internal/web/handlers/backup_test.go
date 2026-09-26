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

func newBackupHandler(t *testing.T) (*handlers.BackupHandler, *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{}
	cfg.SetFilePath(filepath.Join(tmp, "router.yaml"))
	svc := services.NewBackupService(tmp)
	orch := services.NewBackupOrchestrator(svc, cfg)
	return handlers.NewBackupHandler(nil, nil, svc, orch), cfg
}

func TestSaveScheduleRejectsBadCron(t *testing.T) {
	h, _ := newBackupHandler(t)
	form := url.Values{
		"schedule":  {"60 * * * *"}, // minute out of range
		"retention": {"7"},
	}
	req := httptest.NewRequest("POST", "/backup/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleSaveSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSaveSchedulePreservesPassphrase(t *testing.T) {
	h, cfg := newBackupHandler(t)
	cfg.Backup.Passphrase = "existing-secret"

	form := url.Values{
		"enabled":   {"on"},
		"schedule":  {"@daily"},
		"retention": {"5"},
		// passphrase deliberately empty
	}
	req := httptest.NewRequest("POST", "/backup/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleSaveSchedule(rr, req)
	if cfg.Backup.Passphrase != "existing-secret" {
		t.Errorf("passphrase = %q, want preserved", cfg.Backup.Passphrase)
	}
	if !cfg.Backup.Enabled {
		t.Error("enabled not set")
	}
	if cfg.Backup.Schedule != "@daily" {
		t.Errorf("schedule = %q", cfg.Backup.Schedule)
	}
}

func TestAddTargetRejectsDuplicateName(t *testing.T) {
	h, cfg := newBackupHandler(t)
	cfg.Backup.Targets = []config.BackupTarget{{Type: "local", Name: "primary"}}

	form := url.Values{
		"type": {"local"},
		"name": {"primary"},
	}
	req := httptest.NewRequest("POST", "/backup/target", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleAddTarget(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestAddTargetRejectsUnknownType(t *testing.T) {
	h, _ := newBackupHandler(t)
	form := url.Values{
		"type": {"webdav"},
		"name": {"x"},
	}
	req := httptest.NewRequest("POST", "/backup/target", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.HandleAddTarget(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestDeleteTarget404(t *testing.T) {
	h, cfg := newBackupHandler(t)
	cfg.Backup.Targets = []config.BackupTarget{{Type: "local", Name: "primary"}}
	req := httptest.NewRequest("DELETE", "/backup/target/missing", nil)
	rr := httptest.NewRecorder()
	h.HandleDeleteTarget(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
