package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

type IPv6Handler struct {
	renderer  *tmpl.Renderer
	cfg       *config.Config
	ipv6      *services.IPv6Service
	sixinfour *services.SixInFourService
	pppoe     *services.PPPoEService
}

// NewIPv6Handler wires the IPv6 dashboard. sixinfour and pppoe may be
// nil during tests that don't exercise the 6in4 flow; the handler
// nil-checks before reading them.
func NewIPv6Handler(renderer *tmpl.Renderer, cfg *config.Config,
	ipv6 *services.IPv6Service, sixinfour *services.SixInFourService,
	pppoe *services.PPPoEService) *IPv6Handler {
	return &IPv6Handler{
		renderer: renderer, cfg: cfg, ipv6: ipv6,
		sixinfour: sixinfour, pppoe: pppoe,
	}
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

	expiresIn := state.ExpiresIn()
	expiresInText := ""
	if expiresIn > 0 {
		// Display in minutes when below an hour, otherwise hours+minutes.
		if expiresIn < time.Hour {
			expiresInText = fmt.Sprintf("%dm", int(expiresIn.Minutes()))
		} else {
			expiresInText = fmt.Sprintf("%dh%dm",
				int(expiresIn.Hours()),
				int(expiresIn.Minutes())%60)
		}
	}

	pageData := map[string]any{
		"State":        state,
		"Active":       state.Active(),
		"Expired":      state.Expired(),
		"ExpiresIn":    expiresInText,
		"CIDR":         state.CIDR(),
		"AgeMin":       int(state.PrefixAge().Minutes()),
		"WAN":          h.cfg.IPv6.WAN,
		"LAN":          h.cfg.IPv6.LAN,
		"Tunnel":       h.cfg.IPv6.Tunnel,
		"Enabled":      h.cfg.IPv6.Enabled,
		"Mode":         h.cfg.IPv6.Mode,
		"PPPoEUsed":    h.cfg.PPPoE.Username != "",
		"Announced":    announced,
		"ULAPrefix":    h.cfg.IPv6.LAN.ULA.Prefix,
		"ULAEnabled":   h.cfg.IPv6.LAN.ULA.Enabled,
		"RDNSSAddrs":   strings.Fields(state.RDNSS),
		"SearchDomain": h.cfg.System.Domain,
	}

	// In 6in4 mode pull live tunnel state — RX/TX, last DDNS reply,
	// configured prefix — so the status card shows the tunnel plane
	// instead of the (empty) PD lease state.
	if h.cfg.IPv6.Mode == "6in4" && h.sixinfour != nil {
		ts, _ := h.sixinfour.Status(r.Context())
		pageData["TunnelStatus"] = ts
		pageData["TunnelActive"] = ts.Active
	}

	data := &tmpl.PageData{
		Lang: lang,
		Page: "ipv6",
		Data: pageData,
	}

	if err := h.renderer.Render(w, "ipv6", "base", data); err != nil {
		log.Printf("render ipv6: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

// HandleSave persists the IPv6 settings — including a mode swap
// between PD and 6in4 — and re-applies the relevant plane.
func (h *IPv6Handler) HandleSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	requestPrefix := r.FormValue("requestPrefix") == "on"
	rapidCommit := r.FormValue("rapidCommit") == "on"
	hint := strings.TrimSpace(r.FormValue("prefixHint"))
	enabled := r.FormValue("enabled")
	mode := strings.TrimSpace(r.FormValue("mode"))

	switch enabled {
	case "auto", "on", "off":
	default:
		enabled = "auto"
	}
	switch mode {
	case "dhcpv6-pd", "6in4":
	default:
		mode = "dhcpv6-pd"
	}

	if hint != "" && !strings.HasPrefix(hint, "/") {
		hint = "/" + hint
	}

	previousMode := h.cfg.IPv6.Mode
	h.cfg.IPv6.Enabled = enabled
	h.cfg.IPv6.Mode = mode
	h.cfg.IPv6.WAN.RequestPrefix = requestPrefix
	h.cfg.IPv6.WAN.RapidCommit = rapidCommit
	h.cfg.IPv6.WAN.PrefixHint = hint

	// 6in4 fields — only consumed when Mode == "6in4" but stored
	// regardless so the operator's draft survives a mode toggle.
	h.cfg.IPv6.Tunnel.ServerIPv4 = strings.TrimSpace(r.FormValue("tunnelServerIPv4"))
	h.cfg.IPv6.Tunnel.ClientIPv6 = strings.TrimSpace(r.FormValue("tunnelClientIPv6"))
	h.cfg.IPv6.Tunnel.RoutedPrefix = strings.TrimSpace(r.FormValue("tunnelRoutedPrefix"))
	h.cfg.IPv6.Tunnel.TunnelID = strings.TrimSpace(r.FormValue("tunnelID"))
	h.cfg.IPv6.Tunnel.Username = strings.TrimSpace(r.FormValue("tunnelUsername"))
	if v := r.FormValue("tunnelUpdateKey"); v != "" {
		// Empty submit = preserve existing key (form shows placeholder).
		h.cfg.IPv6.Tunnel.UpdateKey = v
	}
	h.cfg.IPv6.Tunnel.AutoUpdate = r.FormValue("tunnelAutoUpdate") == "on"
	if dev := strings.TrimSpace(r.FormValue("tunnelDevice")); dev != "" {
		h.cfg.IPv6.Tunnel.Device = dev
	}

	if err := h.cfg.SaveToFile(); err != nil {
		log.Printf("ipv6 save config: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	// Tear the tunnel down BEFORE the other plane is applied whenever it
	// is no longer wanted, whether that is a mode swap or IPv6 being
	// switched off outright.
	tunnelWanted := enabled != "off" && mode == "6in4"
	if h.sixinfour != nil && !tunnelWanted && previousMode == "6in4" {
		if err := h.sixinfour.Stop(r.Context()); err != nil {
			log.Printf("ipv6: tunnel stop: %v", err)
		}
	}

	if err := h.ipv6.ApplyConfig(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	// Converge the tunnel plane. ApplyConfig consults both the mode and
	// the enabled flag, so a disabled tunnel is stopped rather than
	// rebuilt.
	if h.sixinfour != nil {
		if err := h.sixinfour.ApplyConfig(r.Context()); err != nil {
			log.Printf("6in4 apply on save: %v", err)
			// Don't 500 — the YAML is saved and the operator can
			// inspect the failure on the status card.
		}
	}

	respondRefresh(w, r, "/ipv6")
}

// HandleTunnelUpdateNow asks HE.net to re-register our current IPv4
// endpoint. Used when the operator wants to recover from a stale
// remote IPv4 without waiting for the next PPPoE reconnect.
func (h *IPv6Handler) HandleTunnelUpdateNow(w http.ResponseWriter, r *http.Request) {
	if h.sixinfour == nil {
		clientError(w, r, http.StatusBadRequest, "error.sixInFourNotConfigured")
		return
	}

	currentIPv4 := ""
	if h.pppoe != nil {
		st, _ := h.pppoe.Status(r.Context())
		if st != nil {
			currentIPv4 = st.LocalIP
		}
	}
	if currentIPv4 == "" {
		clientError(w, r, http.StatusServiceUnavailable, "error.noWANIPv4")
		return
	}

	if _, err := h.sixinfour.UpdateRemoteIPv4(r.Context(), currentIPv4); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	h.respondOK(w, r)
}

// HandleSubnetMap accepts a JSON array of names in the desired
// announcement order (e.g. ["lan", "guest", "iot"]). The first entry
// must be "lan" — the primary LAN bridge keeps SLA-ID 0 by contract,
// and the UI pins it as the first row of the sortable table. The
// remaining entries are assigned SLA-IDs 1, 2, 3 ... in submission
// order; the resulting map is persisted via SetSubnetMap, which
// re-renders dhcp6c.conf and the dnsmasq RA drop-in.
func (h *IPv6Handler) HandleSubnetMap(w http.ResponseWriter, r *http.Request) {
	var order []string
	if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidJSON")
		return
	}
	if len(order) == 0 || order[0] != "lan" {
		clientError(w, r, http.StatusBadRequest, "error.firstEntryMustBeLAN")
		return
	}
	m := make(map[string]int, len(order))
	seen := make(map[string]struct{}, len(order))
	for i, name := range order {
		name = strings.TrimSpace(name)
		if name == "" {
			clientError(w, r, http.StatusBadRequest, "error.emptyOrderEntry")
			return
		}
		if _, dup := seen[name]; dup {
			http.Error(w, fmt.Sprintf("duplicate entry %q", name), http.StatusBadRequest)
			return
		}
		seen[name] = struct{}{}
		m[name] = i
	}
	if err := h.ipv6.SetSubnetMap(r.Context(), m); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	h.respondOK(w, r)
}

// HandleRenew triggers an immediate dhcp6c renew (re-solicit the ISP
// without dropping the current lease).
func (h *IPv6Handler) HandleRenew(w http.ResponseWriter, r *http.Request) {
	if err := h.ipv6.Renew(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	h.respondOK(w, r)
}

// HandleRelease drops the current prefix and stops the daemon.
func (h *IPv6Handler) HandleRelease(w http.ResponseWriter, r *http.Request) {
	if err := h.ipv6.Release(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	h.respondOK(w, r)
}

// HandleStart starts the dhcp6c unit.
func (h *IPv6Handler) HandleStart(w http.ResponseWriter, r *http.Request) {
	if err := h.ipv6.Start(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	h.respondOK(w, r)
}

// HandleStop stops the dhcp6c unit (without releasing the lease cleanly).
func (h *IPv6Handler) HandleStop(w http.ResponseWriter, r *http.Request) {
	if err := h.ipv6.Stop(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	h.respondOK(w, r)
}

func (h *IPv6Handler) respondOK(w http.ResponseWriter, r *http.Request) {
	respondRefresh(w, r, "/ipv6")
}
