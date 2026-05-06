package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	if profile := r.FormValue("profile"); profile != "" {
		if profile != "default" && profile != "gaming" && profile != "streaming" && profile != "voip" {
			http.Error(w, "invalid QoS profile", http.StatusBadRequest)
			return
		}
		h.cfg.QoS.Profile = profile
	}
	if upload := r.FormValue("uploadKbps"); upload != "" {
		val, err := strconv.Atoi(upload)
		if err != nil || val < 0 || val > 10000000 {
			http.Error(w, "invalid upload bandwidth", http.StatusBadRequest)
			return
		}
		h.cfg.QoS.UploadKbps = val
	}
	if download := r.FormValue("downloadKbps"); download != "" {
		val, err := strconv.Atoi(download)
		if err != nil || val < 0 || val > 10000000 {
			http.Error(w, "invalid download bandwidth", http.StatusBadRequest)
			return
		}
		h.cfg.QoS.DownloadKbps = val
	}
	if cc := r.FormValue("congestionControl"); cc != "" {
		if cc != "bbr" && cc != "cubic" && cc != "cake" {
			http.Error(w, "invalid congestion control", http.StatusBadRequest)
			return
		}
		h.cfg.QoS.CongestionControl = cc
	}

	h.cfg.QoS.Enabled = r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on"
	if err := h.cfg.SaveToFile(); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

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
	if err := h.cfg.SaveToFile(); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/qos", http.StatusSeeOther)
}
