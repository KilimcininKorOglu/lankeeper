package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type QoSHandler struct {
	renderer *tmpl.Renderer
	qos      *services.QoSService
	cfg      *config.Config
}

func NewQoSHandler(renderer *tmpl.Renderer, qos *services.QoSService, cfg *config.Config) *QoSHandler {
	return &QoSHandler{renderer: renderer, qos: qos, cfg: cfg}
}

func (h *QoSHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	status, _ := h.qos.Status(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "qos",
		Data: map[string]any{
			"Status": status,
			"Config": h.cfg.QoS,
		},
	}

	if err := h.renderer.Render(w, "qos", "base", data); err != nil {
		log.Printf("render qos: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *QoSHandler) HandleApply(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	if profile := r.FormValue("profile"); profile != "" {
		h.cfg.QoS.Profile = profile
	}
	if upload := r.FormValue("uploadKbps"); upload != "" {
		h.cfg.QoS.UploadKbps, _ = strconv.Atoi(upload)
	}
	if download := r.FormValue("downloadKbps"); download != "" {
		h.cfg.QoS.DownloadKbps, _ = strconv.Atoi(download)
	}
	if cc := r.FormValue("congestionControl"); cc != "" {
		h.cfg.QoS.CongestionControl = cc
	}

	h.cfg.QoS.Enabled = r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on"

	if err := h.qos.Apply(r.Context()); err != nil {
		log.Printf("apply qos: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/qos", http.StatusSeeOther)
}

func (h *QoSHandler) HandleClear(w http.ResponseWriter, r *http.Request) {
	if err := h.qos.Clear(r.Context()); err != nil {
		log.Printf("clear qos: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.cfg.QoS.Enabled = false

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/qos", http.StatusSeeOther)
}
