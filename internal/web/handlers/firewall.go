package handlers

import (
	"log"
	"net/http"
	"strconv"

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
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *FirewallHandler) HandleApply(w http.ResponseWriter, r *http.Request) {
	if err := h.firewall.Apply(r.Context()); err != nil {
		log.Printf("apply firewall: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		log.Printf("rollback firewall: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	extPort, err := strconv.Atoi(r.FormValue("extPort"))
	if err != nil || netutil.ValidatePort(extPort) != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	intPort, err := strconv.Atoi(r.FormValue("intPort"))
	if err != nil || netutil.ValidatePort(intPort) != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	protocol := r.FormValue("protocol")
	if protocol != "tcp" && protocol != "udp" && protocol != "both" {
		http.Error(w, "invalid protocol", http.StatusBadRequest)
		return
	}
	intIP := r.FormValue("intIP")
	if netutil.ValidateIP(intIP) != nil {
		http.Error(w, "invalid internal IP", http.StatusBadRequest)
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
		http.Error(w, "save failed", http.StatusInternalServerError)
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
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}

	if err := h.firewall.RemovePortForward(idx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	port, err := strconv.Atoi(r.FormValue("port"))
	if err != nil || netutil.ValidatePort(port) != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	chain := r.FormValue("chain")
	if chain != "input" && chain != "forward" && chain != "output" {
		http.Error(w, "invalid chain", http.StatusBadRequest)
		return
	}
	action := r.FormValue("action")
	if action != "accept" && action != "drop" && action != "reject" {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}
	protocol := r.FormValue("protocol")
	if protocol != "" && protocol != "tcp" && protocol != "udp" && protocol != "icmp" {
		http.Error(w, "invalid protocol", http.StatusBadRequest)
		return
	}
	direction := r.FormValue("direction")
	if direction != "" && direction != "in" && direction != "out" {
		http.Error(w, "invalid direction", http.StatusBadRequest)
		return
	}
	srcIP := r.FormValue("srcIP")
	if srcIP != "" && netutil.ValidateCIDR(srcIP) != nil && netutil.ValidateIP(srcIP) != nil {
		http.Error(w, "invalid source IP/CIDR", http.StatusBadRequest)
		return
	}
	dstIP := r.FormValue("dstIP")
	if dstIP != "" && netutil.ValidateCIDR(dstIP) != nil && netutil.ValidateIP(dstIP) != nil {
		http.Error(w, "invalid destination IP/CIDR", http.StatusBadRequest)
		return
	}

	rule := config.FirewallRule{
		Name:      r.FormValue("name"),
		Chain:     chain,
		Action:    action,
		SrcIP:     srcIP,
		DstIP:     dstIP,
		Protocol:  protocol,
		Port:      port,
		Interface: r.FormValue("interface"),
		Direction: direction,
		Enabled:   true,
	}

	if err := h.firewall.AddRule(rule); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
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
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}

	if err := h.firewall.RemoveRule(idx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") == "true"

	if err := h.firewall.ToggleRule(idx, enabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(r.FormValue("port"))
	if err != nil || netutil.ValidatePort(port) != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	protocol := r.FormValue("protocol")
	if protocol != "tcp" && protocol != "udp" && protocol != "both" {
		http.Error(w, "invalid protocol", http.StatusBadRequest)
		return
	}
	source := r.FormValue("source")
	if source != "" && netutil.ValidateCIDR(source) != nil && netutil.ValidateIP(source) != nil {
		http.Error(w, "invalid source IP/CIDR", http.StatusBadRequest)
		return
	}

	op := config.OpenPort{
		Name:     r.FormValue("name"),
		Protocol: protocol,
		Port:     port,
		Source:   source,
		Enabled:  true,
	}

	if err := h.firewall.AddOpenPort(op); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
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
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}

	if err := h.firewall.RemoveOpenPort(idx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") == "true"

	if err := h.firewall.ToggleOpenPort(idx, enabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}
