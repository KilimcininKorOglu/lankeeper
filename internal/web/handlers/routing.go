package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

type RoutingHandler struct {
	renderer *tmpl.Renderer
	routing  *services.RoutingService
}

func NewRoutingHandler(renderer *tmpl.Renderer, routing *services.RoutingService) *RoutingHandler {
	return &RoutingHandler{renderer: renderer, routing: routing}
}

func (h *RoutingHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "routing",
		Data: map[string]any{
			"Policies": h.routing.GetPolicies(),
		},
	}

	if err := h.renderer.Render(w, "routing", "base", data); err != nil {
		log.Printf("render routing: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *RoutingHandler) HandleAddPolicy(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	policy := config.RoutingPolicy{
		Name:    r.FormValue("name"),
		Enabled: true,
		Tunnel:  r.FormValue("tunnel"),
	}

	if srcMACs := r.FormValue("srcMacs"); srcMACs != "" {
		policy.SrcMACs = strings.Split(srcMACs, ",")
		for _, mac := range policy.SrcMACs {
			if netutil.ValidateMAC(strings.TrimSpace(mac)) != nil {
				clientErrorf(w, r, http.StatusBadRequest, "error.invalidMAC", mac)
				return
			}
		}
	}
	if srcIPs := r.FormValue("srcIps"); srcIPs != "" {
		policy.SrcIPs = strings.Split(srcIPs, ",")
		for _, cidr := range policy.SrcIPs {
			if netutil.ValidateCIDR(strings.TrimSpace(cidr)) != nil {
				clientErrorf(w, r, http.StatusBadRequest, "error.invalidCIDR", cidr)
				return
			}
		}
	}
	if dstIPs := r.FormValue("dstIps"); dstIPs != "" {
		policy.DstIPs = strings.Split(dstIPs, ",")
		for _, cidr := range policy.DstIPs {
			if netutil.ValidateCIDR(strings.TrimSpace(cidr)) != nil {
				clientErrorf(w, r, http.StatusBadRequest, "error.invalidCIDR", cidr)
				return
			}
		}
	}
	if domains := r.FormValue("domains"); domains != "" {
		var cleaned []string
		for _, d := range strings.Split(domains, "\n") {
			d = strings.TrimSpace(d)
			if d != "" {
				cleaned = append(cleaned, d)
			}
		}
		policy.Domains = cleaned
	}

	if err := h.routing.AddPolicy(policy); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	respondRefresh(w, r, "/routing")
}

func (h *RoutingHandler) HandleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.routing.RemovePolicy(name); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	respondRefresh(w, r, "/routing")
}

func (h *RoutingHandler) HandleReorder(w http.ResponseWriter, r *http.Request) {
	var names []string
	if err := json.NewDecoder(r.Body).Decode(&names); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidJSON")
		return
	}

	if err := h.routing.UpdatePriorities(names); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	w.WriteHeader(http.StatusOK)
}
