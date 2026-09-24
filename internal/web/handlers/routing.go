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

	policy, key, bad := parsePolicyForm(r)
	if key != "" {
		clientErrorf(w, r, http.StatusBadRequest, key, bad)
		return
	}

	if err := h.routing.AddPolicy(policy); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	respondRefresh(w, r, "/routing")
}

// parsePolicyForm reads a routing policy from the form. On an invalid
// entry it returns the locale key and the offending entry as submitted.
func parsePolicyForm(r *http.Request) (policy config.RoutingPolicy, key, bad string) {
	macs, bad, ok := splitChecked(r.FormValue("srcMacs"), netutil.ValidateMAC)
	if !ok {
		return policy, "error.invalidMAC", bad
	}
	srcIPs, bad, ok := splitChecked(r.FormValue("srcIps"), netutil.ValidateCIDR)
	if !ok {
		return policy, "error.invalidCIDR", bad
	}
	dstIPs, bad, ok := splitChecked(r.FormValue("dstIps"), netutil.ValidateCIDR)
	if !ok {
		return policy, "error.invalidCIDR", bad
	}
	return config.RoutingPolicy{
		Name:    r.FormValue("name"),
		Enabled: true,
		Tunnel:  r.FormValue("tunnel"),
		SrcMACs: macs,
		SrcIPs:  srcIPs,
		DstIPs:  dstIPs,
		Domains: nonEmptyLines(r.FormValue("domains")),
	}, "", ""
}

// splitChecked splits a comma-separated field and validates each entry
// after trimming it. The entries are returned as split, untrimmed. On
// failure it returns the first invalid entry and false. An empty field
// yields nil.
func splitChecked(raw string, validate func(string) error) (items []string, bad string, ok bool) {
	if raw == "" {
		return nil, "", true
	}
	items = strings.Split(raw, ",")
	for _, item := range items {
		if validate(strings.TrimSpace(item)) != nil {
			return nil, item, false
		}
	}
	return items, "", true
}

// nonEmptyLines returns the trimmed, non-blank lines of raw, or nil when
// there are none.
func nonEmptyLines(raw string) []string {
	var lines []string
	for line := range strings.SplitSeq(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
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
