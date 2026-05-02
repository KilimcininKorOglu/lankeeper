package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
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
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *RoutingHandler) HandleAddPolicy(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	policy := config.RoutingPolicy{
		Name:    r.FormValue("name"),
		Enabled: true,
		Tunnel:  r.FormValue("tunnel"),
	}

	if srcMACs := r.FormValue("srcMacs"); srcMACs != "" {
		policy.SrcMACs = strings.Split(srcMACs, ",")
	}
	if srcIPs := r.FormValue("srcIps"); srcIPs != "" {
		policy.SrcIPs = strings.Split(srcIPs, ",")
	}
	if dstIPs := r.FormValue("dstIps"); dstIPs != "" {
		policy.DstIPs = strings.Split(dstIPs, ",")
	}
	if domains := r.FormValue("domains"); domains != "" {
		policy.Domains = strings.Split(domains, "\n")
	}

	h.routing.AddPolicy(policy)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/routing", http.StatusSeeOther)
}

func (h *RoutingHandler) HandleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.routing.RemovePolicy(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/routing", http.StatusSeeOther)
}

func (h *RoutingHandler) HandleReorder(w http.ResponseWriter, r *http.Request) {
	var names []string
	if err := json.NewDecoder(r.Body).Decode(&names); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	h.routing.UpdatePriorities(names)

	w.WriteHeader(http.StatusOK)
}
