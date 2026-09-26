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

	ttlFix := h.firewall.GetTTLFix()
	data := &tmpl.PageData{
		Lang: lang,
		Page: "firewall",
		Data: map[string]any{
			"OpenPorts":     h.firewall.GetOpenPorts(),
			"PortForwards":  h.firewall.GetPortForwards(),
			"Rules":         h.firewall.GetCustomRules(),
			"TTLFixEnabled": ttlFix.Enabled,
			"TTLFixValue":   ttlFix.Value,
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

	respondTrigger(w, r, "firewallApplied", "/firewall")
}

func (h *FirewallHandler) HandleConfirm(w http.ResponseWriter, r *http.Request) {
	h.firewall.Confirm()

	respondTrigger(w, r, "firewallConfirmed", "/firewall")
}

func (h *FirewallHandler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	if err := h.firewall.Rollback(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	respondTrigger(w, r, "firewallRolledBack", "/firewall")
}

func (h *FirewallHandler) HandleAddPortForward(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	pf, key := parsePortForwardForm(r)
	if key != "" {
		clientError(w, r, http.StatusBadRequest, key)
		return
	}

	if err := h.firewall.AddPortForward(pf); err != nil {
		serverError(w, r, "error.saveFailed", err)
		return
	}

	respondTrigger(w, r, "portForwardAdded", "/firewall")
}

// parsePortForwardForm reads a port forward from the form. The second
// result is the locale key of the first invalid field, or "".
func parsePortForwardForm(r *http.Request) (config.PortForward, string) {
	extPort, extOK := formPort(r, "extPort")
	intPort, intOK := formPort(r, "intPort")
	pf := config.PortForward{
		Name:     r.FormValue("name"),
		Protocol: r.FormValue("protocol"),
		ExtPort:  extPort,
		IntIP:    r.FormValue("intIP"),
		IntPort:  intPort,
		Enabled:  true,
	}
	return pf, firstFailed(
		check{!extOK, "error.invalidPort"},
		check{!intOK, "error.invalidPort"},
		check{!oneOf(pf.Protocol, "tcp", "udp", "both"), "error.invalidProtocol"},
		check{netutil.ValidateIP(pf.IntIP) != nil, "error.invalidInternalIP"},
	)
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

	respondTrigger(w, r, "portForwardDeleted", "/firewall")
}

func (h *FirewallHandler) HandleAddRule(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	rule, key := parseRuleForm(r)
	if key != "" {
		clientError(w, r, http.StatusBadRequest, key)
		return
	}

	if err := h.firewall.AddRule(rule); err != nil {
		serverError(w, r, "error.saveFailed", err)
		return
	}

	respondRefresh(w, r, "/firewall")
}

// parseRuleForm reads a custom rule from the form. The second result is
// the locale key of the first invalid field, or "".
//
// The name and the interface are written verbatim into the nftables
// file: the interface into a quoted match, the name into a trailing
// comment. Neither may carry a quote or a newline.
func parseRuleForm(r *http.Request) (config.FirewallRule, string) {
	port, portOK := formPort(r, "port")
	rule := config.FirewallRule{
		Name:      r.FormValue("name"),
		Chain:     r.FormValue("chain"),
		Action:    r.FormValue("action"),
		SrcIP:     r.FormValue("srcIP"),
		DstIP:     r.FormValue("dstIP"),
		Protocol:  r.FormValue("protocol"),
		Port:      port,
		Interface: r.FormValue("interface"),
		Direction: r.FormValue("direction"),
		Enabled:   true,
	}
	return rule, firstFailed(
		check{!portOK, "error.invalidPort"},
		check{!oneOf(rule.Chain, "input", "forward", "output"), "error.invalidChain"},
		check{!oneOf(rule.Action, "accept", "drop", "reject"), "error.invalidAction"},
		check{!oneOf(rule.Protocol, "", "tcp", "udp", "icmp"), "error.invalidProtocol"},
		check{!oneOf(rule.Direction, "", "in", "out"), "error.invalidDirection"},
		check{!optionalAddress(rule.SrcIP), "error.invalidSourceAddress"},
		check{!optionalAddress(rule.DstIP), "error.invalidDestinationAddress"},
		check{netutil.ValidateRuleName(rule.Name) != nil, "error.invalidRuleName"},
		check{rule.Interface != "" && netutil.ValidateInterfaceName(rule.Interface) != nil, "error.invalidInterface"},
	)
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

	respondRefresh(w, r, "/firewall")
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

	respondRefresh(w, r, "/firewall")
}

func (h *FirewallHandler) HandleAddOpenPort(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	port, portOK := formPort(r, "port")
	op := config.OpenPort{
		Name:      r.FormValue("name"),
		Protocol:  r.FormValue("protocol"),
		Port:      port,
		Source:    r.FormValue("source"),
		Enabled:   true,
		RateLimit: strings.TrimSpace(r.FormValue("rateLimit")),
	}
	if key := firstFailed(
		check{!portOK, "error.invalidPort"},
		check{!oneOf(op.Protocol, "tcp", "udp", "both"), "error.invalidProtocol"},
		check{!optionalAddress(op.Source), "error.invalidSourceAddress"},
		check{services.ValidateOpenPortRateLimit(op.RateLimit) != nil, "error.invalidRateLimit"},
	); key != "" {
		clientError(w, r, http.StatusBadRequest, key)
		return
	}

	if err := h.firewall.AddOpenPort(op); err != nil {
		serverError(w, r, "error.saveFailed", err)
		return
	}

	respondRefresh(w, r, "/firewall")
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

	respondRefresh(w, r, "/firewall")
}

func (h *FirewallHandler) HandleSetTTLFix(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	enabled := r.FormValue("enabled") == "true"

	// The field keeps its stored value when the form omits it, so
	// toggling the checkbox off does not silently reset the hop limit
	// to something the operator never chose.
	value := h.firewall.GetTTLFix().Value
	if raw := strings.TrimSpace(r.FormValue("value")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			clientError(w, r, http.StatusBadRequest, "error.invalidTTL")
			return
		}
		value = parsed
	}

	if err := h.firewall.SetTTLFix(enabled, value); err != nil {
		if errors.Is(err, services.ErrInvalidTTL) {
			clientError(w, r, http.StatusBadRequest, "error.invalidTTL")
			return
		}
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	respondRefresh(w, r, "/firewall")
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

	respondRefresh(w, r, "/firewall")
}
