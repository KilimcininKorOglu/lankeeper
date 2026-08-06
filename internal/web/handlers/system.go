package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
	"golang.org/x/crypto/bcrypt"
)

type SystemHandler struct {
	renderer *tmpl.Renderer
	cfg      *config.Config
	loc      *i18n.I18n
	dhcp     *services.DHCPService
	backup   *services.BackupService
	update   *services.UpdateService
	system   *services.SystemService
	// passwordSink hands a newly generated hash back to the auth
	// object, which caches it by value. A callback rather than a
	// direct reference because Auth lives in the parent web package,
	// which already imports this one. May be nil.
	passwordSink func(hash string)
}

// SetPasswordSink wires the callback that refreshes the live password
// hash, following the same injection pattern as SetDNSService and
// SetRunner elsewhere in the tree.
func (h *SystemHandler) SetPasswordSink(fn func(hash string)) {
	h.passwordSink = fn
}

func NewSystemHandler(renderer *tmpl.Renderer, cfg *config.Config, loc *i18n.I18n, dhcp *services.DHCPService, backup *services.BackupService, update *services.UpdateService, system *services.SystemService) *SystemHandler {
	return &SystemHandler{renderer: renderer, cfg: cfg, loc: loc, dhcp: dhcp, backup: backup, update: update, system: system}
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
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *SystemHandler) HandleChangeWebPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	newPassword := r.FormValue("newPassword")
	confirmPassword := r.FormValue("confirmPassword")

	if newPassword != confirmPassword || len(newPassword) < 8 {
		clientError(w, r, http.StatusBadRequest, "error.passwordMismatchOrShort")
		return
	}

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.internal")
		return
	}

	h.cfg.System.AdminPasswordHash = string(hashBytes)
	if err := h.cfg.SaveToFile(); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	// Persisting alone is not enough: the auth object holds its own
	// copy, so without this the old password would keep working until
	// the process restarted.
	if h.passwordSink != nil {
		h.passwordSink(string(hashBytes))
	}
	log.Println("web UI admin password changed")

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "settingsUpdated")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SystemHandler) HandleChangeRootPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	newPassword := r.FormValue("rootPassword")
	confirmPassword := r.FormValue("rootPasswordConfirm")

	if newPassword != confirmPassword || len(newPassword) < 8 {
		clientError(w, r, http.StatusBadRequest, "error.passwordMismatchOrShort")
		return
	}

	if err := h.system.SetRootPassword(r.Context(), newPassword); err != nil {
		log.Printf("change root password: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.rootPasswordFailed")
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
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	hostname := r.FormValue("hostname")
	domain := r.FormValue("domain")

	if err := services.ValidateHostname(hostname); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidHostname")
		return
	}

	oldDomain := h.cfg.System.Domain
	h.cfg.System.Hostname = hostname
	if domain != "" {
		h.cfg.System.Domain = domain
	}

	if err := h.system.SetHostname(r.Context(), hostname); err != nil {
		log.Printf("system: set hostname: %v", err)
	}

	if domain != "" && domain != oldDomain {
		if h.dhcp != nil {
			if err := h.dhcp.RebuildDNSRecords(context.Background(), h.cfg.System.Domain); err != nil {
				log.Printf("system: rebuild dns records: %v", err)
			}
		}
	}

	if err := h.cfg.SaveToFile(); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}
	log.Printf("hostname changed to %s.%s", hostname, h.cfg.System.Domain)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "settingsUpdated")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SystemHandler) HandleUpdateTimezone(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	tz := r.FormValue("timezone")

	if err := services.ValidateTimezone(tz); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidTimezone")
		return
	}

	h.cfg.System.Timezone = tz
	if err := h.cfg.SaveToFile(); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	if err := h.system.SetTimezone(r.Context(), tz); err != nil {
		log.Printf("system: set timezone: %v", err)
	}

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
	if err := h.system.Reboot(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) HandleFactoryReset(w http.ResponseWriter, r *http.Request) {
	log.Println("factory reset requested via web UI")
	if h.backup != nil {
		if err := h.backup.FactoryReset(r.Context()); err != nil {
			fail(w, r, http.StatusInternalServerError, err)
			return
		}
	}
	if err := h.system.Reboot(r.Context()); err != nil {
		log.Printf("system: reboot: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) HandleExport(w http.ResponseWriter, r *http.Request) {
	passphrase := r.FormValue("passphrase")
	if passphrase == "" {
		clientError(w, r, http.StatusBadRequest, "error.passphraseRequired")
		return
	}

	outputPath := filepath.Join(os.TempDir(), fmt.Sprintf("lankeeper-backup-%s.tar.gz.enc", time.Now().Format("20060102-150405")))

	if err := h.backup.Export(r.Context(), outputPath, passphrase); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.exportFailed")
		return
	}
	defer func() { _ = os.Remove(outputPath) }()

	setSecretDownloadHeaders(w, "application/octet-stream", filepath.Base(outputPath))
	http.ServeFile(w, r, outputPath)
}

// maxBackupUploadBytes caps an imported archive. A real backup holds
// router.yaml plus the Unbound, dnsmasq and OpenVPN directories, so it
// is measured in megabytes; this leaves a wide margin. Without a cap the
// upload was copied into the temp directory in full before anything
// looked at it, so a single admin session could fill the router's disk,
// or its RAM where TMPDIR is tmpfs-backed.
const maxBackupUploadBytes = 64 << 20

// maxBackupFormMemory bounds what the multipart parser keeps in memory;
// the rest spills to temp files, themselves bounded by the reader above.
const maxBackupFormMemory = 4 << 20

func (h *SystemHandler) HandleImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUploadBytes)
	if err := r.ParseMultipartForm(maxBackupFormMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			clientError(w, r, http.StatusRequestEntityTooLarge, "error.backupTooLarge")
			return
		}
		clientError(w, r, http.StatusBadRequest, "error.backupFileRequired")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	file, _, err := r.FormFile("backup")
	if err != nil {
		clientError(w, r, http.StatusBadRequest, "error.backupFileRequired")
		return
	}
	defer func() { _ = file.Close() }()

	tmpFile, err := os.CreateTemp("", "lankeeper-import-*.tar.gz")
	if err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.tempFileFailed")
		return
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, err := io.Copy(tmpFile, file); err != nil {
		_ = tmpFile.Close()
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			clientError(w, r, http.StatusRequestEntityTooLarge, "error.backupTooLarge")
			return
		}
		clientError(w, r, http.StatusInternalServerError, "error.uploadSaveFailed")
		return
	}
	_ = tmpFile.Close()

	passphrase := r.FormValue("passphrase")
	if err := h.backup.Import(r.Context(), tmpFile.Name(), passphrase); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.importFailed")
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
		_, _ = fmt.Fprintf(w, `<div class="alert alert-error">%s: %s</div>`, h.loc.T(lang, "update.error"), html.EscapeString(err.Error()))
		return
	}

	if !info.Available {
		_, _ = fmt.Fprintf(w, `<div class="alert alert-success" style="margin-top:var(--space-md);">%s (%s)</div>`,
			h.loc.T(lang, "update.upToDate"), info.CurrentVersion)
		return
	}

	_, _ = fmt.Fprintf(w, `<div style="margin-top:var(--space-md); padding:var(--space-md); border:1px solid var(--border-color); border-radius:var(--radius-md);">
		<div style="font-weight:700; margin-bottom:var(--space-sm);">%s: %s</div>
		<div style="color:var(--text-secondary); font-size:var(--font-sm); margin-bottom:var(--space-sm);">%s: %s</div>`,
		h.loc.T(lang, "update.available"), html.EscapeString(info.LatestVersion),
		h.loc.T(lang, "update.currentVersion"), html.EscapeString(info.CurrentVersion))

	if info.AssetSize > 0 {
		_, _ = fmt.Fprintf(w, `<div style="color:var(--text-secondary); font-size:var(--font-sm); margin-bottom:var(--space-sm);">%s: %.1f MB</div>`,
			h.loc.T(lang, "update.size"), float64(info.AssetSize)/1024/1024)
	}

	if info.ReleaseNotes != "" {
		_, _ = fmt.Fprintf(w, `<details style="margin-bottom:var(--space-md);"><summary style="cursor:pointer;">%s</summary><pre style="font-size:var(--font-xs); white-space:pre-wrap; margin-top:var(--space-sm);">%s</pre></details>`,
			h.loc.T(lang, "update.releaseNotes"), html.EscapeString(info.ReleaseNotes))
	}

	_, _ = fmt.Fprintf(w, `<button class="btn btn-primary btn-sm" hx-post="/system/update/apply" hx-swap="none" hx-confirm="%s">%s</button></div>`,
		h.loc.T(lang, "update.confirmApply"), h.loc.T(lang, "update.downloadAndInstall"))
}

func (h *SystemHandler) HandleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	info, err := h.update.CheckForUpdate(r.Context())
	if err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	if !info.Available {
		clientError(w, r, http.StatusBadRequest, "error.noUpdateAvailable")
		return
	}
	if err := h.update.ApplyUpdate(r.Context(), info); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) HandleConfirmUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.update.ConfirmUpdate(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
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
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) HandleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(h.update.GetVersionInfo()); err != nil {
		log.Printf("system: encode version: %v", err)
	}
}
