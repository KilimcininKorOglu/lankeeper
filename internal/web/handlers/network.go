package handlers

import (
	"errors"
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
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

	sniffStatus := h.pppoe.SniffStatus()

	data := &tmpl.PageData{
		Lang: lang,
		Page: "network",
		Data: map[string]any{
			"Interfaces":   ifaces,
			"PPPoE":        pppoeStatus,
			"USB":          usbStatus,
			"HealthChecks": healthResults,
			"Sniff":        sniffStatus,
		},
	}

	if err := h.renderer.Render(w, "network", "base", data); err != nil {
		log.Printf("render network: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

// The four handlers below separate two different things the operator
// needs: whether the failover path is permitted at all, and whether it
// is carrying traffic right now. Enable and disable write policy;
// activate and deactivate move the default route. Collapsing them into
// one control would mean an operator could not arm the failover without
// also switching the WAN over to it immediately.

func (h *NetworkHandler) HandleUSBEnable(w http.ResponseWriter, r *http.Request) {
	h.setUSBEnabled(w, r, true)
}

func (h *NetworkHandler) HandleUSBDisable(w http.ResponseWriter, r *http.Request) {
	h.setUSBEnabled(w, r, false)
}

func (h *NetworkHandler) setUSBEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if err := h.usb.SetEnabled(enabled); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/network")
}

func (h *NetworkHandler) HandleUSBAutoFailover(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	if err := h.usb.SetAutoFailover(r.FormValue("enabled") == "true"); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/network")
}

func (h *NetworkHandler) HandleUSBActivate(w http.ResponseWriter, r *http.Request) {
	// A missing interface is the operator's cable, not a fault in the
	// router, so it answers 400 with a message naming the interface
	// rather than a 500 that reads as something being broken.
	if err := h.usb.Activate(r.Context()); err != nil {
		if errors.Is(err, services.ErrPhoneNotConnected) {
			clientError(w, r, http.StatusBadRequest, "error.usbNoPhone")
			return
		}
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/network")
}

func (h *NetworkHandler) HandleUSBDeactivate(w http.ResponseWriter, r *http.Request) {
	if err := h.usb.Deactivate(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/network")
}
