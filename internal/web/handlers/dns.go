package handlers

import (
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/netutil"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

var dnsNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,251}[a-zA-Z0-9]$`)

type DNSHandler struct {
	renderer *tmpl.Renderer
	dns      *services.DNSService
}

func NewDNSHandler(renderer *tmpl.Renderer, dns *services.DNSService) *DNSHandler {
	return &DNSHandler{renderer: renderer, dns: dns}
}

func (h *DNSHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	stats, _ := h.dns.GetStats(r.Context())

	limit := 50
	offset := 0
	if p := r.URL.Query().Get("page"); p != "" {
		page, _ := strconv.Atoi(p)
		if page > 0 {
			offset = (page - 1) * limit
		}
	}

	queries := h.dns.GetRecentQueries(limit, offset)

	data := &tmpl.PageData{
		Lang: lang,
		Page: "dns",
		Data: map[string]any{
			"Stats":         stats,
			"Queries":       queries,
			"StaticRecords": h.dns.GetStaticRecords(),
			"DoTEnabled":    h.dns.GetDNSConfig().EnableDoT,
			"DoTUpstream":   h.dns.GetDNSConfig().DoTUpstream,
		},
	}

	if err := h.renderer.Render(w, "dns", "base", data); err != nil {
		log.Printf("render dns: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *DNSHandler) HandleClearLog(w http.ResponseWriter, r *http.Request) {
	if err := h.dns.ClearQueryLog(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dns", http.StatusSeeOther)
}

func (h *DNSHandler) HandleUpdateBlocklist(w http.ResponseWriter, r *http.Request) {
	if err := h.dns.UpdateBlocklist(r.Context()); err != nil {
		log.Printf("update blocklist: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dns", http.StatusSeeOther)
}

func (h *DNSHandler) HandleSaveDoT(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	enable := r.FormValue("enable_dot") == "on"
	upstream := strings.TrimSpace(r.FormValue("dot_upstream"))
	if enable && upstream == "" {
		http.Error(w, "DoT upstream required when enabled", http.StatusBadRequest)
		return
	}
	if err := h.dns.SaveDNSSettings(enable, upstream); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.dns.ApplyConfig(r.Context()); err != nil {
		log.Printf("dns apply after dot save: %v", err)
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dns", http.StatusSeeOther)
}

func (h *DNSHandler) HandleAddRecord(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	ip := strings.TrimSpace(r.FormValue("ip"))
	localZone := r.FormValue("local_zone") == "on"

	if !dnsNamePattern.MatchString(name) {
		http.Error(w, "invalid DNS name", http.StatusBadRequest)
		return
	}
	if netutil.ValidateIP(ip) != nil {
		http.Error(w, "invalid IP address", http.StatusBadRequest)
		return
	}

	rec := config.StaticDNSRecord{
		Name:           name,
		IP:             ip,
		LocalZone:      localZone,
		DisableAutoPTR: r.FormValue("disable_auto_ptr") == "on",
		Source:         config.DNSSourceUser,
	}
	if err := h.dns.AddStaticRecord(rec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.dns.ApplyConfig(r.Context()); err != nil {
		log.Printf("dns apply after add record: %v", err)
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dns", http.StatusSeeOther)
}

func (h *DNSHandler) HandleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}
	if err := h.dns.RemoveStaticRecord(idx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.dns.ApplyConfig(r.Context()); err != nil {
		log.Printf("dns apply after delete record: %v", err)
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dns", http.StatusSeeOther)
}
