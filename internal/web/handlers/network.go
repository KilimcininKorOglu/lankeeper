package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

type NetworkHandler struct {
	renderer  *tmpl.Renderer
	network   *services.NetworkService
	pppoe     *services.PPPoEService
	usb       *services.USBTetheringService
	health    *services.HealthCheckService
	firstBoot *services.FirstBootService
}

func NewNetworkHandler(
	renderer *tmpl.Renderer,
	network *services.NetworkService,
	pppoe *services.PPPoEService,
	usb *services.USBTetheringService,
	health *services.HealthCheckService,
	firstBoot *services.FirstBootService,
) *NetworkHandler {
	return &NetworkHandler{
		renderer:  renderer,
		network:   network,
		pppoe:     pppoe,
		usb:       usb,
		health:    health,
		firstBoot: firstBoot,
	}
}

// HandleCompleteFirstBoot ends first-boot mode.
//
// It is an explicit operator action because nothing else in the system
// can tell when the interface config is finished: there is no event to
// hang it off, and first-boot mode has a real cost while it lasts. The
// bridge carries the future WAN card, so an ISP line plugged in while
// it is up puts the upstream segment on the LAN bridge.
func (h *NetworkHandler) HandleCompleteFirstBoot(w http.ResponseWriter, r *http.Request) {
	if err := h.firstBoot.Complete(r.Context()); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	respondRefresh(w, r, "/network")
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
			"Interfaces": ifaces,
			// The configured entries, separate from the detected NICs.
			// The operator needs both side by side: the config is what
			// every service renders against, and the detected list is
			// the only place the real device names appear.
			"ConfiguredInterfaces": h.network.ConfiguredInterfaces(),
			"PPPoE":                pppoeStatus,
			"USB":                  usbStatus,
			"HealthChecks":         healthResults,
			"Sniff":                sniffStatus,
			"FirstBoot":            h.firstBoot.IsActive(),
			"FirstBootWAN":         h.firstBoot.WANDevices(),
		},
	}

	if err := h.renderer.Render(w, "network", "base", data); err != nil {
		log.Printf("render network: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

// HandleSetInterface adds or updates one interface entry.
//
// This is the first thing an operator has to do on unfamiliar
// hardware: the shipped config names the NICs of the machine it was
// built on, so until these can be corrected from the UI the rest of the
// product is configured against devices that do not exist.
func (h *NetworkHandler) HandleSetInterface(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	mtu := 0
	if raw := strings.TrimSpace(r.FormValue("mtu")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			clientError(w, r, http.StatusBadRequest, "error.invalidMTU")
			return
		}
		mtu = parsed
	}

	in := config.InterfaceConfig{
		ID:      strings.TrimSpace(r.FormValue("id")),
		Device:  strings.TrimSpace(r.FormValue("device")),
		Label:   strings.TrimSpace(r.FormValue("label")),
		Role:    r.FormValue("role"),
		Type:    r.FormValue("type"),
		Address: strings.TrimSpace(r.FormValue("address")),
		MTU:     mtu,
		IPv6:    r.FormValue("ipv6"),
	}

	if err := h.network.SetInterface(in); err != nil {
		h.interfaceError(w, r, err)
		return
	}

	respondRefresh(w, r, "/network")
}

func (h *NetworkHandler) HandleRemoveInterface(w http.ResponseWriter, r *http.Request) {
	if err := h.network.RemoveInterface(r.PathValue("id")); err != nil {
		h.interfaceError(w, r, err)
		return
	}
	respondRefresh(w, r, "/network")
}

// interfaceError maps the refusals the operator can act on to 400 with
// a specific message. Everything else is a 500, so a genuine fault is
// not disguised as a form mistake.
func (h *NetworkHandler) interfaceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrLastLANInterface):
		clientError(w, r, http.StatusBadRequest, "error.lastLanInterface")
	case errors.Is(err, services.ErrDuplicateInterface):
		clientError(w, r, http.StatusBadRequest, "error.duplicateInterfaceDevice")
	case errors.Is(err, services.ErrInterfaceNotFound):
		clientError(w, r, http.StatusNotFound, "error.interfaceNotFound")
	case errors.Is(err, services.ErrInvalidRole):
		clientError(w, r, http.StatusBadRequest, "error.invalidRole")
	case errors.Is(err, services.ErrInvalidType):
		clientError(w, r, http.StatusBadRequest, "error.invalidType")
	default:
		clientError(w, r, http.StatusBadRequest, "error.invalidInterface")
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
