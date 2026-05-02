package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type NTPHandler struct {
	renderer *tmpl.Renderer
	ntp      *services.NTPService
}

func NewNTPHandler(renderer *tmpl.Renderer, ntp *services.NTPService) *NTPHandler {
	return &NTPHandler{renderer: renderer, ntp: ntp}
}

func (h *NTPHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	status, _ := h.ntp.GetStatus(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "ntp",
		Data: map[string]any{
			"Status": status,
		},
	}

	if err := h.renderer.Render(w, "ntp", "base", data); err != nil {
		log.Printf("render ntp: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *NTPHandler) HandleForceSync(w http.ResponseWriter, r *http.Request) {
	if err := h.ntp.ForceSync(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/ntp", http.StatusSeeOther)
}
