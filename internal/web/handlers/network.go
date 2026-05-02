package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type NetworkHandler struct {
	renderer *tmpl.Renderer
	network  *services.NetworkService
	pppoe    *services.PPPoEService
	usb      *services.USBTetheringService
	health   *services.HealthCheckService
}

func NewNetworkHandler(
	renderer *tmpl.Renderer,
	network *services.NetworkService,
	pppoe *services.PPPoEService,
	usb *services.USBTetheringService,
	health *services.HealthCheckService,
) *NetworkHandler {
	return &NetworkHandler{
		renderer: renderer,
		network:  network,
		pppoe:    pppoe,
		usb:      usb,
		health:   health,
	}
}

func (h *NetworkHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	ifaces, err := h.network.DetectInterfaces()
	if err != nil {
		log.Printf("detect interfaces: %v", err)
	}

	pppoeStatus, err := h.pppoe.Status(r.Context())
	if err != nil {
		log.Printf("pppoe status: %v", err)
	}

	usbStatus, err := h.usb.Status(r.Context())
	if err != nil {
		log.Printf("usb status: %v", err)
	}

	healthResults := h.health.GetResults()

	data := &tmpl.PageData{
		Lang: lang,
		Page: "network",
		Data: map[string]any{
			"Interfaces":    ifaces,
			"PPPoE":         pppoeStatus,
			"USB":           usbStatus,
			"HealthChecks":  healthResults,
		},
	}

	if err := h.renderer.Render(w, "network", "base", data); err != nil {
		log.Printf("render network: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
