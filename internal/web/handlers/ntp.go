package handlers

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
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
			"Config": h.ntp.GetConfig(),
		},
	}

	if err := h.renderer.Render(w, "ntp", "base", data); err != nil {
		log.Printf("render ntp: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *NTPHandler) HandleForceSync(w http.ResponseWriter, r *http.Request) {
	if err := h.ntp.ForceSync(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/ntp")
}

func (h *NTPHandler) HandleAddSource(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	host := strings.TrimSpace(r.FormValue("host"))
	if host == "" {
		clientError(w, r, http.StatusBadRequest, "error.hostRequired")
		return
	}
	if err := h.ntp.AddSource(host); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	if err := h.ntp.ApplyConfig(r.Context()); err != nil {
		log.Printf("ntp apply after add source: %v", err)
	}
	respondRefresh(w, r, "/ntp")
}

func (h *NTPHandler) HandleRemoveSource(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidIndex")
		return
	}
	if err := h.ntp.RemoveSource(idx); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	if err := h.ntp.ApplyConfig(r.Context()); err != nil {
		log.Printf("ntp apply after remove source: %v", err)
	}
	respondRefresh(w, r, "/ntp")
}

func (h *NTPHandler) HandleAddAllowSubnet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	cidr := strings.TrimSpace(r.FormValue("cidr"))
	if netutil.ValidateCIDR(cidr) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidCIDR")
		return
	}
	if err := h.ntp.AddAllowSubnet(cidr); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	if err := h.ntp.ApplyConfig(r.Context()); err != nil {
		log.Printf("ntp apply after add allow: %v", err)
	}
	respondRefresh(w, r, "/ntp")
}

func (h *NTPHandler) HandleRemoveAllowSubnet(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidIndex")
		return
	}
	if err := h.ntp.RemoveAllowSubnet(idx); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	if err := h.ntp.ApplyConfig(r.Context()); err != nil {
		log.Printf("ntp apply after remove allow: %v", err)
	}
	respondRefresh(w, r, "/ntp")
}

func (h *NTPHandler) HandleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	fallback := r.FormValue("fallback")
	listenAddress := r.FormValue("listen_address")
	listenPort, _ := strconv.Atoi(r.FormValue("listen_port"))
	serverEnabled := r.FormValue("server_enabled") == "on"
	rtcSync := r.FormValue("rtc_sync") == "on"
	if err := h.ntp.SaveSettings(fallback, listenAddress, listenPort, serverEnabled, rtcSync); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	if err := h.ntp.ApplyConfig(r.Context()); err != nil {
		log.Printf("ntp apply after save settings: %v", err)
	}
	respondRefresh(w, r, "/ntp")
}
