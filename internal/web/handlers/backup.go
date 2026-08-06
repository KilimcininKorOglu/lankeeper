package handlers

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

// BackupHandler owns the /backup pages: schedule + targets + history.
// Kept separate from SystemHandler because the surface area is large
// (5 routes, a target editor modal, a history table) and adding it
// to SystemHandler would push that file past the project's "one
// concern per handler" convention.
type BackupHandler struct {
	renderer *tmpl.Renderer
	cfg      *config.Config
	loc      *i18n.I18n
	backup   *services.BackupService
}

func NewBackupHandler(renderer *tmpl.Renderer, cfg *config.Config, loc *i18n.I18n, backup *services.BackupService) *BackupHandler {
	return &BackupHandler{renderer: renderer, cfg: cfg, loc: loc, backup: backup}
}

// HandleBackupPage renders the backup schedule + history page.
func (h *BackupHandler) HandleBackupPage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())
	data := &tmpl.PageData{
		Lang: lang,
		Page: "backup",
		Data: map[string]any{
			"Backup":   h.cfg.Backup,
			"Targets":  h.cfg.Backup.Targets,
			"History":  reverseHistory(h.cfg.Backup.History),
			"HasPass":  h.cfg.Backup.Passphrase != "",
		},
	}
	if err := h.renderer.Render(w, "backup", "default", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleSaveSchedule accepts cron schedule, retention, passphrase
// and the enabled toggle. Passphrase is preserved when the form
// field is empty so an operator editing schedule alone doesn't
// blank the encryption credential.
func (h *BackupHandler) HandleSaveSchedule(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") == "on" || r.FormValue("enabled") == "true"
	schedule := strings.TrimSpace(r.FormValue("schedule"))
	retention, _ := strconv.Atoi(r.FormValue("retention"))
	if retention < 1 {
		retention = 7
	}
	passphrase := r.FormValue("passphrase")

	if schedule != "" {
		if _, err := services.ParseSchedule(schedule, nil); err != nil {
			http.Error(w, "invalid schedule: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	h.cfg.Backup.Enabled = enabled
	h.cfg.Backup.Schedule = schedule
	h.cfg.Backup.Retention = retention
	if passphrase != "" {
		h.cfg.Backup.Passphrase = passphrase
	}
	if err := h.cfg.SaveToFile(); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/backup", http.StatusSeeOther)
}

// HandleAddTarget appends a new target. Type-specific fields are
// read selectively so the operator only fills out what their
// chosen target needs.
func (h *BackupHandler) HandleAddTarget(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	for _, t := range h.cfg.Backup.Targets {
		if t.Name == name {
			http.Error(w, "duplicate name", http.StatusBadRequest)
			return
		}
	}
	target := config.BackupTarget{
		Type: r.FormValue("type"),
		Name: name,
	}
	switch target.Type {
	case "local":
		target.Path = strings.TrimSpace(r.FormValue("path"))
	case "s3":
		target.Endpoint = strings.TrimSpace(r.FormValue("endpoint"))
		target.Region = strings.TrimSpace(r.FormValue("region"))
		target.Bucket = strings.TrimSpace(r.FormValue("bucket"))
		target.Prefix = strings.TrimSpace(r.FormValue("prefix"))
		target.AccessKeyID = strings.TrimSpace(r.FormValue("accessKeyId"))
		target.SecretAccessKey = r.FormValue("secretAccessKey")
		target.UsePathStyle = r.FormValue("usePathStyle") == "on"
	case "sftp":
		target.Host = strings.TrimSpace(r.FormValue("host"))
		target.Port, _ = strconv.Atoi(r.FormValue("port"))
		target.User = strings.TrimSpace(r.FormValue("user"))
		target.Password = r.FormValue("password")
		target.KeyPath = strings.TrimSpace(r.FormValue("keyPath"))
		target.RemoteDir = strings.TrimSpace(r.FormValue("remoteDir"))
		target.HostKeyFingerprint = strings.TrimSpace(r.FormValue("hostKeyFingerprint"))
	default:
		http.Error(w, "invalid type", http.StatusBadRequest)
		return
	}
	h.cfg.Backup.Targets = append(h.cfg.Backup.Targets, target)
	if err := h.cfg.SaveToFile(); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/backup", http.StatusSeeOther)
}

// HandleDeleteTarget removes a target by name. Idempotent - 404 if
// no match. Mostly used via HTMX from the targets table.
func (h *BackupHandler) HandleDeleteTarget(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/backup/target/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	out := make([]config.BackupTarget, 0, len(h.cfg.Backup.Targets))
	found := false
	for _, t := range h.cfg.Backup.Targets {
		if t.Name == name {
			found = true
			continue
		}
		out = append(out, t)
	}
	if !found {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	h.cfg.Backup.Targets = out
	if err := h.cfg.SaveToFile(); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/backup", http.StatusSeeOther)
}

// HandleRunNow fires an immediate backup. Synchronous so the
// operator sees errors directly. Long-running uploads will block
// the HTTP request; we accept this tradeoff for v1 since
// encrypted-config archives are typically a few MB.
func (h *BackupHandler) HandleRunNow(w http.ResponseWriter, r *http.Request) {
	if err := h.backup.RunNow(r.Context()); err != nil {
		_, _ = fmt.Fprintf(w, `<div class="alert alert-error">%s</div>`, html.EscapeString(err.Error()))
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/backup", http.StatusSeeOther)
}

// HandleHistory returns the last N runs as JSON for HTMX-driven
// polling. Used by the page's auto-refreshing history table.
func (h *BackupHandler) HandleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reverseHistory(h.cfg.Backup.History))
}

// reverseHistory returns the history slice with the newest entry
// first. We persist in chronological order so the YAML reads in a
// natural top-down sequence, but UIs always want the latest at the
// top of the table.
func reverseHistory(history []config.BackupHistory) []config.BackupHistory {
	out := make([]config.BackupHistory, len(history))
	for i, e := range history {
		out[len(history)-1-i] = e
	}
	return out
}
