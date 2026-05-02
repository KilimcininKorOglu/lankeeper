package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
)

type SyslogHandler struct {
	renderer *tmpl.Renderer
	syslog   *services.SyslogService
}

func NewSyslogHandler(renderer *tmpl.Renderer, syslog *services.SyslogService) *SyslogHandler {
	return &SyslogHandler{renderer: renderer, syslog: syslog}
}

func (h *SyslogHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	hosts, _ := h.syslog.GetRemoteHosts(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "syslog",
		Data: map[string]any{
			"Hosts": hosts,
		},
	}

	if err := h.renderer.Render(w, "syslog", "base", data); err != nil {
		log.Printf("render syslog: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
