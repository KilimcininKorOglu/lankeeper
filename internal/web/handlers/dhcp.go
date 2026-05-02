package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type DHCPHandler struct {
	renderer *tmpl.Renderer
	dhcp     *services.DHCPService
}

func NewDHCPHandler(renderer *tmpl.Renderer, dhcp *services.DHCPService) *DHCPHandler {
	return &DHCPHandler{renderer: renderer, dhcp: dhcp}
}

func (h *DHCPHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	leases, _ := h.dhcp.GetLeases()

	data := &tmpl.PageData{
		Lang: lang,
		Page: "dhcp",
		Data: map[string]any{
			"Leases":       leases,
			"StaticLeases": h.dhcp.GetDeviceList(),
		},
	}

	if err := h.renderer.Render(w, "dhcp", "base", data); err != nil {
		log.Printf("render dhcp: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *DHCPHandler) HandleAddStatic(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	h.dhcp.AddStaticLease(r.FormValue("mac"), r.FormValue("ip"), r.FormValue("hostname"))

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dhcp", http.StatusSeeOther)
}
