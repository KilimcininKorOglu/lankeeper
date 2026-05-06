package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
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
			"StaticLeases": h.dhcp.GetStaticLeases(),
		},
	}

	if err := h.renderer.Render(w, "dhcp", "base", data); err != nil {
		log.Printf("render dhcp: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *DHCPHandler) HandleAddStatic(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	mac := r.FormValue("mac")
	ip := r.FormValue("ip")
	hostname := r.FormValue("hostname")

	if netutil.ValidateMAC(mac) != nil {
		http.Error(w, "invalid MAC address", http.StatusBadRequest)
		return
	}
	if netutil.ValidateIP(ip) != nil {
		http.Error(w, "invalid IP address", http.StatusBadRequest)
		return
	}
	if hostname == "" {
		http.Error(w, "hostname required", http.StatusBadRequest)
		return
	}

	if err := h.dhcp.AddStaticLease(mac, ip, hostname); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if err := h.dhcp.ApplyConfig(r.Context()); err != nil {
		log.Printf("dhcp apply after add: %v", err)
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dhcp", http.StatusSeeOther)
}

func (h *DHCPHandler) HandleDeleteStatic(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}

	if err := h.dhcp.RemoveStaticLease(idx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.dhcp.ApplyConfig(r.Context()); err != nil {
		log.Printf("dhcp apply after delete: %v", err)
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dhcp", http.StatusSeeOther)
}
