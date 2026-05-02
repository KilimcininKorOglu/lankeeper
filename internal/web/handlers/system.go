package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
	"golang.org/x/crypto/bcrypt"
)

type SystemHandler struct {
	renderer *tmpl.Renderer
	cfg      *config.Config
}

func NewSystemHandler(renderer *tmpl.Renderer, cfg *config.Config) *SystemHandler {
	return &SystemHandler{renderer: renderer, cfg: cfg}
}

func (h *SystemHandler) HandleSettingsPage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "settings",
		Data: map[string]any{
			"Hostname": h.cfg.System.Hostname,
			"Timezone": h.cfg.System.Timezone,
			"Language": h.cfg.System.Language,
			"TLSMode":  h.cfg.System.TLS.Mode,
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

	_, err := netutil.Run(context.Background(), "chpasswd")
	if err != nil {
		input := fmt.Sprintf("root:%s", newPassword)
		_, err = netutil.Run(context.Background(), "bash", "-c",
			fmt.Sprintf("echo '%s' | chpasswd", input))
		if err != nil {
			log.Printf("change root password: %v", err)
			http.Error(w, "Failed to change root password", http.StatusInternalServerError)
			return
		}
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

	if hostname == "" || len(hostname) > 63 {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	h.cfg.System.Hostname = hostname

	netutil.Run(context.Background(), "hostnamectl", "set-hostname", hostname)

	log.Printf("hostname changed to %s", hostname)

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

	netutil.Run(context.Background(), "timedatectl", "set-timezone", tz)

	log.Printf("timezone changed to %s", tz)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "settingsUpdated")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
