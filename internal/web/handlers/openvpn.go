package handlers

import (
	"errors"
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
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *OpenVPNHandler) HandleInitPKI(w http.ResponseWriter, r *http.Request) {
	if err := h.ovpn.InitPKI(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/openvpn")
}

func (h *OpenVPNHandler) HandleAddClient(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	name := r.FormValue("name")
	if name == "" {
		clientError(w, r, http.StatusBadRequest, "error.nameRequired")
		return
	}
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidNameCharacters")
		return
	}

	peerType := r.FormValue("peerType")
	siteToSite := peerType == "site-to-site"
	fixedIP := r.FormValue("fixedIP")
	if fixedIP != "" {
		if err := netutil.ValidateIP(fixedIP); err != nil {
			clientErrorf(w, r, http.StatusBadRequest, "error.invalidFixedIP", err.Error())
			return
		}
	}

	var remoteSubnets []string
	if raw := strings.TrimSpace(r.FormValue("remoteSubnets")); raw != "" && siteToSite {
		for s := range strings.SplitSeq(raw, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				if err := netutil.ValidateCIDR(trimmed); err != nil {
					clientErrorf(w, r, http.StatusBadRequest, "error.invalidRemoteSubnet", trimmed)
					return
				}
				remoteSubnets = append(remoteSubnets, trimmed)
			}
		}
	}

	if err := h.ovpn.AddClient(r.Context(), name, siteToSite, remoteSubnets, fixedIP); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	respondRefresh(w, r, "/openvpn")
}

func (h *OpenVPNHandler) HandleDownloadOVPN(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidClientName")
		return
	}

	ovpnContent, err := h.ovpn.GenerateClientOVPN(name)
	if err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.generateFailed")
		return
	}

	setSecretDownloadHeaders(w, "application/x-openvpn-profile", name+".ovpn")
	_, _ = w.Write([]byte(ovpnContent))
}

func (h *OpenVPNHandler) HandleServerStart(w http.ResponseWriter, r *http.Request) {
	// "Already running" treated as a no-op; see VPN handler comment
	// for rationale.
	if err := h.ovpn.ServerStart(r.Context()); err != nil && !errors.Is(err, services.ErrOpenVPNAlreadyRunning) {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/openvpn")
}

func (h *OpenVPNHandler) HandleServerStop(w http.ResponseWriter, r *http.Request) {
	if err := h.ovpn.ServerStop(r.Context()); err != nil && !errors.Is(err, services.ErrOpenVPNAlreadyStopped) {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/openvpn")
}

func (h *OpenVPNHandler) HandleRevokeClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidClientName")
		return
	}
	if err := h.ovpn.RevokeClient(r.Context(), name); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	respondRefresh(w, r, "/openvpn")
}

func (h *OpenVPNHandler) HandleAddOutboundClient(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	name := r.FormValue("name")
	if name == "" {
		clientError(w, r, http.StatusBadRequest, "error.nameRequired")
		return
	}
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidNameCharacters")
		return
	}

	remoteHost := r.FormValue("remoteHost")
	if remoteHost == "" {
		clientError(w, r, http.StatusBadRequest, "error.remoteHostRequired")
		return
	}

	protocol := r.FormValue("protocol")
	if protocol != "udp" && protocol != "tcp" {
		clientError(w, r, http.StatusBadRequest, "error.protocolUDPOrTCP")
		return
	}

	cipher := r.FormValue("cipher")
	if cipher != "" && !validCiphers[cipher] {
		clientError(w, r, http.StatusBadRequest, "error.invalidCipher")
		return
	}

	auth := r.FormValue("auth")
	if auth != "" && !validAuths[auth] {
		clientError(w, r, http.StatusBadRequest, "error.invalidAuth")
		return
	}

	rawConfig := r.FormValue("configFile")
	port, err := strconv.Atoi(r.FormValue("remotePort"))
	if err != nil || netutil.ValidatePort(port) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidPort")
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
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	respondRefresh(w, r, "/openvpn")
}

func (h *OpenVPNHandler) HandleConnectOutbound(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidClientName")
		return
	}
	if err := h.ovpn.ConnectClient(r.Context(), name); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/openvpn")
}

func (h *OpenVPNHandler) HandleDisconnectOutbound(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if len(name) > 64 || !ovpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidClientName")
		return
	}
	if err := h.ovpn.DisconnectClient(r.Context(), name); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/openvpn")
}
