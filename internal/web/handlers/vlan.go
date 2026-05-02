package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type VLANHandler struct {
	renderer *tmpl.Renderer
	network  *services.NetworkService
	cfg      *config.Config
}

func NewVLANHandler(renderer *tmpl.Renderer, network *services.NetworkService, cfg *config.Config) *VLANHandler {
	return &VLANHandler{renderer: renderer, network: network, cfg: cfg}
}

func (h *VLANHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "network",
		Data: map[string]any{
			"VLANs":      h.cfg.VLANs,
			"Interfaces": h.cfg.Interfaces,
		},
	}

	if r.Header.Get("HX-Request") == "true" {
		h.renderer.RenderPartial(w, "network", "vlan_list", data)
		return
	}

	if err := h.renderer.Render(w, "network", "base", data); err != nil {
		log.Printf("render network (vlan): %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *VLANHandler) HandleAdd(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	vid, _ := strconv.Atoi(r.FormValue("vid"))
	mtu, _ := strconv.Atoi(r.FormValue("mtu"))
	if mtu == 0 {
		mtu = 1500
	}

	vlan := config.VLANConfig{
		ID:       r.FormValue("id"),
		Parent:   r.FormValue("parent"),
		VID:      vid,
		Label:    r.FormValue("label"),
		Role:     r.FormValue("role"),
		Type:     r.FormValue("type"),
		Address:  r.FormValue("address"),
		MTU:      mtu,
		Isolated: r.FormValue("isolated") == "true" || r.FormValue("isolated") == "on",
	}

	if dhcpEnabled := r.FormValue("dhcpEnabled"); dhcpEnabled == "true" || dhcpEnabled == "on" {
		vlan.DHCP = config.VLANDHCPConfig{
			Enabled:    true,
			RangeStart: r.FormValue("dhcpRangeStart"),
			RangeEnd:   r.FormValue("dhcpRangeEnd"),
			LeaseTime:  r.FormValue("dhcpLeaseTime"),
		}
	}

	h.cfg.VLANs = append(h.cfg.VLANs, vlan)

	var parentDev string
	for _, iface := range h.cfg.Interfaces {
		if iface.ID == vlan.Parent {
			parentDev = iface.Device
			break
		}
	}

	if parentDev != "" {
		if err := h.network.CreateVLAN(r.Context(), parentDev, vlan.VID, vlan.Address, vlan.MTU); err != nil {
			log.Printf("create VLAN %d: %v", vlan.VID, err)
		}
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/network", http.StatusSeeOther)
}

func (h *VLANHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	for i, v := range h.cfg.VLANs {
		if v.ID == id {
			var parentDev string
			for _, iface := range h.cfg.Interfaces {
				if iface.ID == v.Parent {
					parentDev = iface.Device
					break
				}
			}
			if parentDev != "" {
				h.network.DeleteVLAN(r.Context(), parentDev, v.VID)
			}

			h.cfg.VLANs = append(h.cfg.VLANs[:i], h.cfg.VLANs[i+1:]...)
			break
		}
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/network", http.StatusSeeOther)
}
