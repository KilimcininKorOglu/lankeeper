package handlers

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type OpenVPNHandler struct {
	renderer *tmpl.Renderer
	ovpn     *services.OpenVPNService
}

func NewOpenVPNHandler(renderer *tmpl.Renderer, ovpn *services.OpenVPNService) *OpenVPNHandler {
	return &OpenVPNHandler{renderer: renderer, ovpn: ovpn}
}

func (h *OpenVPNHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	status, _ := h.ovpn.ServerStatus(r.Context())
	clients := h.ovpn.ListServerClients()
	outbound := h.ovpn.ListOutboundClients()

	data := &tmpl.PageData{
		Lang: lang,
		Page: "openvpn",
		Data: map[string]any{
			"Server":   status,
			"Clients":  clients,
			"Outbound": outbound,
		},
	}

	if err := h.renderer.Render(w, "openvpn", "base", data); err != nil {
		log.Printf("render openvpn: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *OpenVPNHandler) HandleInitPKI(w http.ResponseWriter, r *http.Request) {
	if err := h.ovpn.InitPKI(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/openvpn", http.StatusSeeOther)
}

func (h *OpenVPNHandler) HandleAddClient(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	peerType := r.FormValue("peerType")
	siteToSite := peerType == "site-to-site"
	fixedIP := r.FormValue("fixedIP")

	var remoteSubnets []string
	if raw := strings.TrimSpace(r.FormValue("remoteSubnets")); raw != "" && siteToSite {
		for _, s := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				remoteSubnets = append(remoteSubnets, trimmed)
			}
		}
	}

	if err := h.ovpn.AddClient(r.Context(), name, siteToSite, remoteSubnets, fixedIP); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/openvpn", http.StatusSeeOther)
}

func (h *OpenVPNHandler) HandleDownloadOVPN(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ovpnContent, err := h.ovpn.GenerateClientOVPN(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-openvpn-profile")
	w.Header().Set("Content-Disposition", "attachment; filename="+name+".ovpn")
	w.Write([]byte(ovpnContent))
}

func (h *OpenVPNHandler) HandleServerStart(w http.ResponseWriter, r *http.Request) {
	if err := h.ovpn.ServerStart(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/openvpn", http.StatusSeeOther)
}

func (h *OpenVPNHandler) HandleServerStop(w http.ResponseWriter, r *http.Request) {
	if err := h.ovpn.ServerStop(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/openvpn", http.StatusSeeOther)
}

func (h *OpenVPNHandler) HandleRevokeClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.ovpn.RevokeClient(r.Context(), name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/openvpn", http.StatusSeeOther)
}

func (h *OpenVPNHandler) HandleAddOutboundClient(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	rawConfig := r.FormValue("configFile")
	port, _ := strconv.Atoi(r.FormValue("remotePort"))

	client := config.OVPNClientConfig{
		Name:       name,
		ConfigFile: rawConfig,
		RemoteHost: r.FormValue("remoteHost"),
		RemotePort: port,
		Protocol:   r.FormValue("protocol"),
		Cipher:     r.FormValue("cipher"),
		Auth:       r.FormValue("auth"),
		TLSAuth:    r.FormValue("tlsAuth") == "true",
		Username:   r.FormValue("username"),
		Password:   r.FormValue("password"),
	}

	h.ovpn.AddOutboundClient(client)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/openvpn", http.StatusSeeOther)
}

func (h *OpenVPNHandler) HandleConnectOutbound(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.ovpn.ConnectClient(r.Context(), name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/openvpn", http.StatusSeeOther)
}
