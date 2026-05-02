package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

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
			"Stats":   stats,
			"Queries": queries,
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
