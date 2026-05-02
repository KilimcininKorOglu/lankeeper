package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
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
			"PortForwards":  h.cfg.Firewall.PortForwards,
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
	r.ParseForm()

	extPort, _ := strconv.Atoi(r.FormValue("extPort"))
	intPort, _ := strconv.Atoi(r.FormValue("intPort"))

	pf := config.PortForward{
		Name:     r.FormValue("name"),
		Protocol: r.FormValue("protocol"),
		ExtPort:  extPort,
		IntIP:    r.FormValue("intIP"),
		IntPort:  intPort,
		Enabled:  true,
	}

	h.firewall.AddPortForward(pf)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "portForwardAdded")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/firewall", http.StatusSeeOther)
}

func (h *FirewallHandler) HandleDeletePortForward(w http.ResponseWriter, r *http.Request) {
	idx, _ := strconv.Atoi(r.PathValue("index"))

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
