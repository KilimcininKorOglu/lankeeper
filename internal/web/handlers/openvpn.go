package handlers

import (
	"log"
	"net/http"

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

	data := &tmpl.PageData{
		Lang: lang,
		Page: "openvpn",
		Data: map[string]any{
			"Server": status,
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

	if err := h.ovpn.AddClient(r.Context(), name); err != nil {
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
