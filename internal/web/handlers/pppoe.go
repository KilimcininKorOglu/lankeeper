package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type PPPoEHandler struct {
	renderer *tmpl.Renderer
	pppoe    *services.PPPoEService
}

func NewPPPoEHandler(renderer *tmpl.Renderer, pppoe *services.PPPoEService) *PPPoEHandler {
	return &PPPoEHandler{renderer: renderer, pppoe: pppoe}
}

func (h *PPPoEHandler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	if err := h.pppoe.Connect(r.Context()); err != nil {
		log.Printf("pppoe connect: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/network", http.StatusSeeOther)
}

func (h *PPPoEHandler) HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := h.pppoe.Disconnect(r.Context()); err != nil {
		log.Printf("pppoe disconnect: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/network", http.StatusSeeOther)
}

func (h *PPPoEHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())
	status, _ := h.pppoe.Status(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Data: map[string]any{"PPPoE": status},
	}

	if err := h.renderer.RenderPartial(w, "network", "wan-status", data); err != nil {
		log.Printf("render wan-status: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
