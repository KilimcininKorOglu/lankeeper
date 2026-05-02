package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type HealthCheckHandler struct {
	renderer *tmpl.Renderer
	health   *services.HealthCheckService
}

func NewHealthCheckHandler(renderer *tmpl.Renderer, health *services.HealthCheckService) *HealthCheckHandler {
	return &HealthCheckHandler{renderer: renderer, health: health}
}

func (h *HealthCheckHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())
	results := h.health.GetResults()

	data := &tmpl.PageData{
		Lang: lang,
		Data: map[string]any{"HealthChecks": results},
	}

	if err := h.renderer.RenderPartial(w, "network", "healthcheck", data); err != nil {
		log.Printf("render healthcheck: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *HealthCheckHandler) HandleReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	h.health.ResetCounter(name)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/network", http.StatusSeeOther)
}
