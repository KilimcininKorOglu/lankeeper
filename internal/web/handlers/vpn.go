package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type VPNHandler struct {
	renderer *tmpl.Renderer
	vpn      *services.VPNService
}

func NewVPNHandler(renderer *tmpl.Renderer, vpn *services.VPNService) *VPNHandler {
	return &VPNHandler{renderer: renderer, vpn: vpn}
}

func (h *VPNHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	tunnels, _ := h.vpn.ListClientTunnels(r.Context())
	serverStatus, _ := h.vpn.ServerStatus(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "vpn",
		Data: map[string]any{
			"Tunnels": tunnels,
			"Server":  serverStatus,
		},
	}

	if err := h.renderer.Render(w, "vpn", "base", data); err != nil {
		log.Printf("render vpn: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *VPNHandler) HandleAddPeer(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	peerType := r.FormValue("peerType")
	siteToSite := peerType == "site-to-site"
	endpoint := r.FormValue("endpoint")

	var remoteSubnets []string
	if raw := strings.TrimSpace(r.FormValue("remoteSubnets")); raw != "" && siteToSite {
		for _, s := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				remoteSubnets = append(remoteSubnets, trimmed)
			}
		}
	}

	peer, privKey, err := h.vpn.AddPeer(r.Context(), name, siteToSite, remoteSubnets, endpoint)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	confStr := h.vpn.GeneratePeerConfig(peer, privKey)

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename="+name+".conf")
	w.Write([]byte(confStr))
}

func (h *VPNHandler) HandleRemovePeer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.vpn.RemovePeer(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/vpn", http.StatusSeeOther)
}
