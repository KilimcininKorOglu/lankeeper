package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
	"golang.org/x/crypto/bcrypt"
)

type SystemHandler struct {
	renderer *tmpl.Renderer
	cfg      *config.Config
	loc      *i18n.I18n
	dhcp     *services.DHCPService
	backup   *services.BackupService
	update   *services.UpdateService
}

func NewSystemHandler(renderer *tmpl.Renderer, cfg *config.Config, loc *i18n.I18n, dhcp *services.DHCPService, backup *services.BackupService, update *services.UpdateService) *SystemHandler {
	return &SystemHandler{renderer: renderer, cfg: cfg, loc: loc, dhcp: dhcp, backup: backup, update: update}
}

func (h *SystemHandler) HandleSettingsPage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "settings",
		Data: map[string]any{
			"Hostname":       h.cfg.System.Hostname,
			"Domain":         h.cfg.System.Domain,
			"FQDN":           h.cfg.System.Hostname + "." + h.cfg.System.Domain,
			"Timezone":       h.cfg.System.Timezone,
			"Language":       h.cfg.System.Language,
			"TLSMode":        h.cfg.System.TLS.Mode,
			"Version":        h.update.GetVersionInfo(),
			"PendingUpdate":  h.update.HasPendingUpdate(),
			"PendingVersion": h.update.PendingVersion(),
		},
	}

	if err := h.renderer.Render(w, "settings", "base", data); err != nil {
		log.Printf("render settings: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *SystemHandler) HandleChangeWebPassword(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	newPassword := r.FormValue("newPassword")
	confirmPassword := r.FormValue("confirmPassword")

	if newPassword != confirmPassword || len(newPassword) < 8 {
		http.Error(w, "Password mismatch or too short (min 8 chars)", http.StatusBadRequest)
		return
	}

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.cfg.System.AdminPasswordHash = string(hashBytes)
	h.cfg.SaveToFile()
	log.Println("web UI admin password changed")

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "settingsUpdated")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SystemHandler) HandleChangeRootPassword(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	newPassword := r.FormValue("rootPassword")
	confirmPassword := r.FormValue("rootPasswordConfirm")

	if newPassword != confirmPassword || len(newPassword) < 8 {
		http.Error(w, "Password mismatch or too short (min 8 chars)", http.StatusBadRequest)
		return
	}

	hashOut, err := netutil.RunSimple(context.Background(), "openssl", "passwd", "-6", newPassword)
	if err != nil {
		log.Printf("generate password hash: %v", err)
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}
	cryptHash := strings.TrimSpace(hashOut)

	if _, err := netutil.Run(context.Background(), "usermod", "-p", cryptHash, "root"); err != nil {
		log.Printf("change root password: %v", err)
		http.Error(w, "Failed to change root password", http.StatusInternalServerError)
		return
	}

	log.Println("root password changed via web UI")

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "settingsUpdated")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SystemHandler) HandleUpdateHostname(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	hostname := r.FormValue("hostname")
	domain := r.FormValue("domain")

	if hostname == "" || len(hostname) > 63 {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	oldDomain := h.cfg.System.Domain
	h.cfg.System.Hostname = hostname
	if domain != "" {
		h.cfg.System.Domain = domain
	}

	netutil.Run(context.Background(), "hostnamectl", "set-hostname", hostname)

	if domain != "" && domain != oldDomain {
		if h.dhcp != nil {
			h.dhcp.RebuildDNSRecords(context.Background(), h.cfg.System.Domain)
		}
	}

	h.cfg.SaveToFile()
	log.Printf("hostname changed to %s.%s", hostname, h.cfg.System.Domain)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "settingsUpdated")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SystemHandler) HandleUpdateTimezone(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	tz := r.FormValue("timezone")

	if tz == "" {
		http.Error(w, "Invalid timezone", http.StatusBadRequest)
		return
	}

	h.cfg.System.Timezone = tz
	h.cfg.SaveToFile()

	netutil.Run(context.Background(), "timedatectl", "set-timezone", tz)

	log.Printf("timezone changed to %s", tz)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "settingsUpdated")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SystemHandler) HandleReboot(w http.ResponseWriter, r *http.Request) {
	log.Println("system reboot requested via web UI")
	_, err := netutil.Run(r.Context(), "systemctl", "reboot")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) HandleFactoryReset(w http.ResponseWriter, r *http.Request) {
	log.Println("factory reset requested via web UI")
	if h.backup != nil {
		if err := h.backup.FactoryReset(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	netutil.Run(r.Context(), "systemctl", "reboot")
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) HandleExport(w http.ResponseWriter, r *http.Request) {
	outputPath := filepath.Join(os.TempDir(), fmt.Sprintf("home-router-backup-%s.tar.gz", time.Now().Format("20060102-150405")))

	if err := h.backup.Export(r.Context(), outputPath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(outputPath)))
	http.ServeFile(w, r, outputPath)
	os.Remove(outputPath)
}

func (h *SystemHandler) HandleImport(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("backup")
	if err != nil {
		http.Error(w, "backup file required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmpFile, err := os.CreateTemp("", "home-router-import-*.tar.gz")
	if err != nil {
		http.Error(w, "failed to create temp file", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		http.Error(w, "failed to save uploaded file", http.StatusInternalServerError)
		return
	}
	tmpFile.Close()

	if err := h.backup.Import(r.Context(), tmpFile.Name()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Println("config imported via web UI")
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SystemHandler) HandleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())
	info, err := h.update.CheckForUpdate(r.Context())
	if err != nil {
		fmt.Fprintf(w, `<div class="alert alert-error">%s: %s</div>`, h.loc.T(lang, "update.error"), err.Error())
		return
	}

	if !info.Available {
		fmt.Fprintf(w, `<div class="alert alert-success" style="margin-top:var(--space-md);">%s (%s)</div>`,
			h.loc.T(lang, "update.upToDate"), info.CurrentVersion)
		return
	}

	fmt.Fprintf(w, `<div style="margin-top:var(--space-md); padding:var(--space-md); border:1px solid var(--border-color); border-radius:var(--radius-md);">
		<div style="font-weight:700; margin-bottom:var(--space-sm);">%s: %s</div>
		<div style="color:var(--text-secondary); font-size:var(--font-sm); margin-bottom:var(--space-sm);">%s: %s</div>`,
		h.loc.T(lang, "update.available"), info.LatestVersion,
		h.loc.T(lang, "update.currentVersion"), info.CurrentVersion)

	if info.AssetSize > 0 {
		fmt.Fprintf(w, `<div style="color:var(--text-secondary); font-size:var(--font-sm); margin-bottom:var(--space-sm);">%s: %.1f MB</div>`,
			h.loc.T(lang, "update.size"), float64(info.AssetSize)/1024/1024)
	}

	if info.ReleaseNotes != "" {
		fmt.Fprintf(w, `<details style="margin-bottom:var(--space-md);"><summary style="cursor:pointer;">%s</summary><pre style="font-size:var(--font-xs); white-space:pre-wrap; margin-top:var(--space-sm);">%s</pre></details>`,
			h.loc.T(lang, "update.releaseNotes"), html.EscapeString(info.ReleaseNotes))
	}

	fmt.Fprintf(w, `<button class="btn btn-primary btn-sm" hx-post="/system/update/apply" hx-swap="none" hx-confirm="%s">%s</button></div>`,
		h.loc.T(lang, "update.confirmApply"), h.loc.T(lang, "update.downloadAndInstall"))
}

func (h *SystemHandler) HandleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	info, err := h.update.CheckForUpdate(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !info.Available {
		http.Error(w, "no update available", http.StatusBadRequest)
		return
	}
	if err := h.update.ApplyUpdate(r.Context(), info); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) HandleConfirmUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.update.ConfirmUpdate(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SystemHandler) HandleRollbackUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.update.Rollback(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) HandleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.update.GetVersionInfo())
}
