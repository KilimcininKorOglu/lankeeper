package handlers

import (
	"net/http"

	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
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
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/network")
}

func (h *PPPoEHandler) HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := h.pppoe.Disconnect(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/network")
}

func (h *PPPoEHandler) HandleSniffStart(w http.ResponseWriter, r *http.Request) {
	if err := h.pppoe.SniffStart(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/network")
}

func (h *PPPoEHandler) HandleSniffStop(w http.ResponseWriter, r *http.Request) {
	if err := h.pppoe.SniffStop(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/network")
}
