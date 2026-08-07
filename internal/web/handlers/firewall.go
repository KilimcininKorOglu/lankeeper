package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

type FirewallHandler struct {
	renderer *tmpl.Renderer
	firewall *services.FirewallService
	cfg      *config.Config
}

func NewFirewallHandler(renderer *tmpl.Renderer, firewall *services.FirewallService, cfg *config.Config) *FirewallHandler {
	return &FirewallHandler{
		renderer: renderer,
		firewall: firewall,
		cfg:      cfg,
	}
}

func (h *FirewallHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "firewall",
		Data: map[string]any{
			"OpenPorts":     h.firewall.GetOpenPorts(),
			"PortForwards":  h.cfg.Firewall.PortForwards,
			"Rules":         h.firewall.GetCustomRules(),
			"TTLFixEnabled": h.cfg.Firewall.TTLFix.Enabled,
			"TTLFixValue":   h.cfg.Firewall.TTLFix.Value,
			"PendingChange": h.firewall.HasPendingChange(),
		},
	}

	if err := h.renderer.Render(w, "firewall", "base", data); err != nil {
		log.Printf("render firewall: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *FirewallHandler) HandleApply(w http.ResponseWriter, r *http.Request) {
	if err := h.firewall.Apply(r.Context()); err != nil {
		// A pending change is a state the operator can resolve from
		// this same page, not a server fault.
		status := http.StatusInternalServerError
		if errors.Is(err, services.ErrChangePending) {
			status = http.StatusConflict
		}
		fail(w, r, status, err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "firewallApplied")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleConfirm(w http.ResponseWriter, r *http.Request) {
	h.firewall.Confirm()

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "firewallConfirmed")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	if err := h.firewall.Rollback(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "firewallRolledBack")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleAddPortForward(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	extPort, err := strconv.Atoi(r.FormValue("extPort"))
	if err != nil || netutil.ValidatePort(extPort) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidPort")
		return
	}
	intPort, err := strconv.Atoi(r.FormValue("intPort"))
	if err != nil || netutil.ValidatePort(intPort) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidPort")
		return
	}

	protocol := r.FormValue("protocol")
	if protocol != "tcp" && protocol != "udp" && protocol != "both" {
		clientError(w, r, http.StatusBadRequest, "error.invalidProtocol")
		return
	}
	intIP := r.FormValue("intIP")
	if netutil.ValidateIP(intIP) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidInternalIP")
		return
	}

	pf := config.PortForward{
		Name:     r.FormValue("name"),
		Protocol: protocol,
		ExtPort:  extPort,
		IntIP:    intIP,
		IntPort:  intPort,
		Enabled:  true,
	}

	if err := h.firewall.AddPortForward(pf); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "portForwardAdded")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleDeletePortForward(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidIndex")
		return
	}

	if err := h.firewall.RemovePortForward(idx); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "portForwardDeleted")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleAddRule(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	port, err := strconv.Atoi(r.FormValue("port"))
	if err != nil || netutil.ValidatePort(port) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidPort")
		return
	}

	chain := r.FormValue("chain")
	if chain != "input" && chain != "forward" && chain != "output" {
		clientError(w, r, http.StatusBadRequest, "error.invalidChain")
		return
	}
	action := r.FormValue("action")
	if action != "accept" && action != "drop" && action != "reject" {
		clientError(w, r, http.StatusBadRequest, "error.invalidAction")
		return
	}
	protocol := r.FormValue("protocol")
	if protocol != "" && protocol != "tcp" && protocol != "udp" && protocol != "icmp" {
		clientError(w, r, http.StatusBadRequest, "error.invalidProtocol")
		return
	}
	direction := r.FormValue("direction")
	if direction != "" && direction != "in" && direction != "out" {
		clientError(w, r, http.StatusBadRequest, "error.invalidDirection")
		return
	}
	srcIP := r.FormValue("srcIP")
	if srcIP != "" && netutil.ValidateCIDR(srcIP) != nil && netutil.ValidateIP(srcIP) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidSourceAddress")
		return
	}
	dstIP := r.FormValue("dstIP")
	if dstIP != "" && netutil.ValidateCIDR(dstIP) != nil && netutil.ValidateIP(dstIP) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidDestinationAddress")
		return
	}

	// Both values are written verbatim into the nftables file: the
	// interface into a quoted match, the name into a trailing comment.
	// Neither may carry a quote or a newline.
	name := r.FormValue("name")
	if err := netutil.ValidateRuleName(name); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidRuleName")
		return
	}
	iface := r.FormValue("interface")
	if iface != "" && netutil.ValidateInterfaceName(iface) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidInterface")
		return
	}

	rule := config.FirewallRule{
		Name:      name,
		Chain:     chain,
		Action:    action,
		SrcIP:     srcIP,
		DstIP:     dstIP,
		Protocol:  protocol,
		Port:      port,
		Interface: iface,
		Direction: direction,
		Enabled:   true,
	}

	if err := h.firewall.AddRule(rule); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleDeleteRule(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidIndex")
		return
	}

	if err := h.firewall.RemoveRule(idx); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleToggleRule(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidIndex")
		return
	}
	enabled := r.FormValue("enabled") == "true"

	if err := h.firewall.ToggleRule(idx, enabled); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleAddOpenPort(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	port, err := strconv.Atoi(r.FormValue("port"))
	if err != nil || netutil.ValidatePort(port) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidPort")
		return
	}

	protocol := r.FormValue("protocol")
	if protocol != "tcp" && protocol != "udp" && protocol != "both" {
		clientError(w, r, http.StatusBadRequest, "error.invalidProtocol")
		return
	}
	source := r.FormValue("source")
	if source != "" && netutil.ValidateCIDR(source) != nil && netutil.ValidateIP(source) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidSourceAddress")
		return
	}

	rateLimit := strings.TrimSpace(r.FormValue("rateLimit"))
	if err := services.ValidateOpenPortRateLimit(rateLimit); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidRateLimit")
		return
	}

	op := config.OpenPort{
		Name:      r.FormValue("name"),
		Protocol:  protocol,
		Port:      port,
		Source:    source,
		Enabled:   true,
		RateLimit: rateLimit,
	}

	if err := h.firewall.AddOpenPort(op); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleDeleteOpenPort(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidIndex")
		return
	}

	if err := h.firewall.RemoveOpenPort(idx); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleToggleOpenPort(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidIndex")
		return
	}
	enabled := r.FormValue("enabled") == "true"

	if err := h.firewall.ToggleOpenPort(idx, enabled); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}
