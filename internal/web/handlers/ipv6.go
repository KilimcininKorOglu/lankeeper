package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

type IPv6Handler struct {
	renderer *tmpl.Renderer
	cfg      *config.Config
	ipv6     *services.IPv6Service
}

func NewIPv6Handler(renderer *tmpl.Renderer, cfg *config.Config, ipv6 *services.IPv6Service) *IPv6Handler {
	return &IPv6Handler{renderer: renderer, cfg: cfg, ipv6: ipv6}
}

// HandlePage renders the IPv6 dashboard with current PD status, the
// LAN/WAN configuration, and the action buttons.
func (h *IPv6Handler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	state, err := h.ipv6.Status(r.Context())
	if err != nil {
		log.Printf("ipv6 status: %v", err)
	}

	announced, err := h.ipv6.AnnouncedInterfaces()
	if err != nil {
		// Misconfiguration (no LAN, bad prefix hint) shouldn't blank
		// the whole page — log and serve an empty list.
		log.Printf("ipv6 announced: %v", err)
	}

	data := &tmpl.PageData{
		Lang: lang,
		Page: "ipv6",
		Data: map[string]any{
			"State":     state,
			"Active":    state.Active(),
			"CIDR":      state.CIDR(),
			"AgeMin":    int(state.PrefixAge().Minutes()),
			"WAN":       h.cfg.IPv6.WAN,
			"LAN":       h.cfg.IPv6.LAN,
			"Enabled":   h.cfg.IPv6.Enabled,
			"Mode":      h.cfg.IPv6.Mode,
			"PPPoEUsed": h.cfg.PPPoE.Username != "",
			"Announced": announced,
		},
	}

	if err := h.renderer.Render(w, "ipv6", "base", data); err != nil {
		log.Printf("render ipv6: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// HandleSave persists the IPv6 WAN settings (request prefix, hint,
// rapid-commit) and re-applies the dhcp6c config.
func (h *IPv6Handler) HandleSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	requestPrefix := r.FormValue("requestPrefix") == "on"
	rapidCommit := r.FormValue("rapidCommit") == "on"
	hint := strings.TrimSpace(r.FormValue("prefixHint"))
	enabled := r.FormValue("enabled")

	switch enabled {
	case "auto", "on", "off":
	default:
		enabled = "auto"
	}

	if hint != "" && !strings.HasPrefix(hint, "/") {
		hint = "/" + hint
	}

	h.cfg.IPv6.Enabled = enabled
	h.cfg.IPv6.WAN.RequestPrefix = requestPrefix
	h.cfg.IPv6.WAN.RapidCommit = rapidCommit
	h.cfg.IPv6.WAN.PrefixHint = hint

	if err := h.cfg.SaveToFile(); err != nil {
		log.Printf("ipv6 save config: %v", err)
		http.Error(w, "config save failed", http.StatusInternalServerError)
		return
	}

	if err := h.ipv6.ApplyConfig(r.Context()); err != nil {
		log.Printf("ipv6 apply: %v", err)
		// Config was saved; surface the apply error but do not roll back
		// the YAML — operator can retry from the UI without re-entering.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/ipv6", http.StatusSeeOther)
}

// HandleRenew triggers an immediate dhcp6c renew (re-solicit the ISP
// without dropping the current lease).
func (h *IPv6Handler) HandleRenew(w http.ResponseWriter, r *http.Request) {
	if err := h.ipv6.Renew(r.Context()); err != nil {
		log.Printf("ipv6 renew: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.respondOK(w, r)
}

// HandleRelease drops the current prefix and stops the daemon.
func (h *IPv6Handler) HandleRelease(w http.ResponseWriter, r *http.Request) {
	if err := h.ipv6.Release(r.Context()); err != nil {
		log.Printf("ipv6 release: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.respondOK(w, r)
}

// HandleStart starts the dhcp6c unit.
func (h *IPv6Handler) HandleStart(w http.ResponseWriter, r *http.Request) {
	if err := h.ipv6.Start(r.Context()); err != nil {
		log.Printf("ipv6 start: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.respondOK(w, r)
}

// HandleStop stops the dhcp6c unit (without releasing the lease cleanly).
func (h *IPv6Handler) HandleStop(w http.ResponseWriter, r *http.Request) {
	if err := h.ipv6.Stop(r.Context()); err != nil {
		log.Printf("ipv6 stop: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.respondOK(w, r)
}

func (h *IPv6Handler) respondOK(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/ipv6", http.StatusSeeOther)
}
