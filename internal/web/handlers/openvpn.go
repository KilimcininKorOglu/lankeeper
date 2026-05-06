package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

var ovpnNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

var validCiphers = map[string]bool{
	"AES-256-GCM":       true,
	"AES-128-GCM":       true,
	"AES-256-CBC":       true,
	"CHACHA20-POLY1305": true,
}

var validAuths = map[string]bool{
	"SHA256": true,
	"SHA384": true,
	"SHA512": true,
}

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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		http.Error(w, "name must be alphanumeric, dashes, or underscores (max 64 chars)", http.StatusBadRequest)
		return
	}

	peerType := r.FormValue("peerType")
	siteToSite := peerType == "site-to-site"
	fixedIP := r.FormValue("fixedIP")
	if fixedIP != "" {
		if err := netutil.ValidateIP(fixedIP); err != nil {
			http.Error(w, "invalid fixedIP: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var remoteSubnets []string
	if raw := strings.TrimSpace(r.FormValue("remoteSubnets")); raw != "" && siteToSite {
		for _, s := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				if err := netutil.ValidateCIDR(trimmed); err != nil {
					http.Error(w, "invalid CIDR in remoteSubnets: "+trimmed, http.StatusBadRequest)
					return
				}
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
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		http.Error(w, "invalid client name", http.StatusBadRequest)
		return
	}

	ovpnContent, err := h.ovpn.GenerateClientOVPN(name)
	if err != nil {
		http.Error(w, "generate failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-openvpn-profile")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.ovpn"`, name))
	_, _ = w.Write([]byte(ovpnContent))
}

func (h *OpenVPNHandler) HandleServerStart(w http.ResponseWriter, r *http.Request) {
	// "Already running" treated as a no-op; see VPN handler comment
	// for rationale.
	if err := h.ovpn.ServerStart(r.Context()); err != nil && !errors.Is(err, services.ErrOpenVPNAlreadyRunning) {
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
	if err := h.ovpn.ServerStop(r.Context()); err != nil && !errors.Is(err, services.ErrOpenVPNAlreadyStopped) {
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		http.Error(w, "name must be alphanumeric, dashes, or underscores (max 64 chars)", http.StatusBadRequest)
		return
	}

	remoteHost := r.FormValue("remoteHost")
	if remoteHost == "" {
		http.Error(w, "remoteHost required", http.StatusBadRequest)
		return
	}

	protocol := r.FormValue("protocol")
	if protocol != "udp" && protocol != "tcp" {
		http.Error(w, "protocol must be udp or tcp", http.StatusBadRequest)
		return
	}

	cipher := r.FormValue("cipher")
	if cipher != "" && !validCiphers[cipher] {
		http.Error(w, "invalid cipher", http.StatusBadRequest)
		return
	}

	auth := r.FormValue("auth")
	if auth != "" && !validAuths[auth] {
		http.Error(w, "invalid auth", http.StatusBadRequest)
		return
	}

	rawConfig := r.FormValue("configFile")
	port, err := strconv.Atoi(r.FormValue("remotePort"))
	if err != nil || netutil.ValidatePort(port) != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	client := config.OVPNClientConfig{
		Name:       name,
		ConfigFile: rawConfig,
		RemoteHost: remoteHost,
		RemotePort: port,
		Protocol:   protocol,
		Cipher:     cipher,
		Auth:       auth,
		TLSAuth:    r.FormValue("tlsAuth") == "true",
		Username:   r.FormValue("username"),
		Password:   r.FormValue("password"),
	}

	if err := h.ovpn.AddOutboundClient(client); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

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

func (h *OpenVPNHandler) HandleDisconnectOutbound(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.ovpn.DisconnectClient(r.Context(), name); err != nil {
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
