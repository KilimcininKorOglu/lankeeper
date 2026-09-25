package handlers

import (
	"log"
	"net/http"
	"slices"
	"strconv"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
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
		if err := h.renderer.RenderPartial(w, "network", "vlan_list", data); err != nil {
			log.Printf("render vlan_list: %v", err)
		}
		return
	}

	if err := h.renderer.Render(w, "network", "base", data); err != nil {
		log.Printf("render network (vlan): %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *VLANHandler) HandleAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	vlan, key := parseVLANForm(r)
	if key != "" {
		clientError(w, r, http.StatusBadRequest, key)
		return
	}
	if err := h.network.ValidateVLAN(vlan); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	if !h.saveVLANs(append(slices.Clip(h.cfg.VLANs), vlan)) {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	if parentDev := h.parentDevice(vlan.Parent); parentDev != "" {
		if err := h.network.CreateVLAN(r.Context(), parentDev, vlan.VID, vlan.Address, vlan.MTU); err != nil {
			log.Printf("create VLAN %d: %v", vlan.VID, err)
		}
	}

	respondRefresh(w, r, "/network")
}

// parseVLANForm reads a VLAN from the form. The second result is the
// locale key of the first invalid field, or "". An empty or zero MTU
// becomes 1500.
func parseVLANForm(r *http.Request) (config.VLANConfig, string) {
	vid, err := strconv.Atoi(r.FormValue("vid"))
	if err != nil || netutil.ValidateVLANID(vid) != nil {
		return config.VLANConfig{}, "error.invalidVLANID"
	}
	mtu, err := strconv.Atoi(r.FormValue("mtu"))
	if err != nil && r.FormValue("mtu") != "" {
		return config.VLANConfig{}, "error.invalidMTU"
	}
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
		Isolated: oneOf(r.FormValue("isolated"), "true", "on"),
	}
	if oneOf(r.FormValue("dhcpEnabled"), "true", "on") {
		vlan.DHCP = config.VLANDHCPConfig{
			Enabled:    true,
			RangeStart: r.FormValue("dhcpRangeStart"),
			RangeEnd:   r.FormValue("dhcpRangeEnd"),
			LeaseTime:  r.FormValue("dhcpLeaseTime"),
		}
	}
	return vlan, ""
}

// parentDevice returns the device of the interface with the given ID,
// or "" when no interface has it.
func (h *VLANHandler) parentDevice(parentID string) string {
	for _, iface := range h.cfg.Interfaces {
		if iface.ID == parentID {
			return iface.Device
		}
	}
	return ""
}

func (h *VLANHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	i := slices.IndexFunc(h.cfg.VLANs, func(v config.VLANConfig) bool { return v.ID == id })
	if i < 0 {
		respondRefresh(w, r, "/network")
		return
	}
	v := h.cfg.VLANs[i]

	// Persist first, so a failed save leaves the device and the entry in
	// place together.
	if !h.saveVLANs(slices.Delete(slices.Clone(h.cfg.VLANs), i, i+1)) {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}
	if parentDev := h.parentDevice(v.Parent); parentDev != "" {
		if err := h.network.DeleteVLAN(r.Context(), parentDev, v.VID); err != nil {
			log.Printf("vlan: delete %s.%d: %v", parentDev, v.VID, err)
		}
	}

	respondRefresh(w, r, "/network")
}

// saveVLANs stores next as the VLAN list and writes the config. When the
// write fails it restores the previous list and logs the error, so the
// running config does not hold a change the file on disk lacks. next must
// not share a backing array with the current list.
func (h *VLANHandler) saveVLANs(next []config.VLANConfig) bool {
	prev := h.cfg.VLANs
	h.cfg.VLANs = next
	if err := h.cfg.SaveToFile(); err != nil {
		h.cfg.VLANs = prev
		log.Printf("vlan: save config: %v", err)
		return false
	}
	return true
}
