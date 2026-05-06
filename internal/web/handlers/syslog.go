package handlers

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

// allowedFacilities is the set of standard syslog facility names accepted
// by the AddFacility handler. RFC 5424 + Linux locals.
var allowedFacilities = map[string]bool{
	"auth": true, "authpriv": true, "cron": true, "daemon": true,
	"kern": true, "lpr": true, "mail": true, "news": true,
	"syslog": true, "user": true,
	"local0": true, "local1": true, "local2": true, "local3": true,
	"local4": true, "local5": true, "local6": true, "local7": true,
}

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
	logs, _ := h.syslog.GetRecentLogs(r.Context(), 100)

	data := &tmpl.PageData{
		Lang: lang,
		Page: "syslog",
		Data: map[string]any{
			"Hosts":      hosts,
			"Config":     h.syslog.GetConfig(),
			"RecentLogs": logs,
		},
	}

	if err := h.renderer.Render(w, "syslog", "base", data); err != nil {
		log.Printf("render syslog: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *SyslogHandler) HandleSaveServerConfig(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cfg := config.SyslogServerConfig{
		Enabled:      r.FormValue("enabled") == "on",
		ListenUDP:    strings.TrimSpace(r.FormValue("listen_udp")),
		ListenTCP:    strings.TrimSpace(r.FormValue("listen_tcp")),
		EnableTLS:    r.FormValue("enable_tls") == "on",
		TLSCertFile:  strings.TrimSpace(r.FormValue("tls_cert_file")),
		TLSKeyFile:   strings.TrimSpace(r.FormValue("tls_key_file")),
		TLSCAFile:    strings.TrimSpace(r.FormValue("tls_ca_file")),
		LogPath:      strings.TrimSpace(r.FormValue("log_path")),
		PerHostDirs:  r.FormValue("per_host_dirs") == "on",
		MaxRetention: strings.TrimSpace(r.FormValue("max_retention")),
	}
	if err := h.syslog.SaveServerConfig(cfg); err != nil {
		// SaveServerConfig validates TLS paths against an allowlist
		// so a rejection here is operator input, not an I/O fault —
		// return 400 so HTMX surfaces the message.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.syslog.ApplyConfig(r.Context()); err != nil {
		log.Printf("syslog apply after save server: %v", err)
	}
	syslogResponse(w, r)
}

func (h *SyslogHandler) HandleSaveClientConfig(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	current := h.syslog.GetConfig().Client
	current.Enabled = r.FormValue("enabled") == "on"
	current.RemoteHost = strings.TrimSpace(r.FormValue("remote_host"))
	proto := strings.TrimSpace(r.FormValue("protocol"))
	if proto != "tcp" {
		proto = "udp"
	}
	current.Protocol = proto
	current.EnableTLS = r.FormValue("enable_tls") == "on"
	current.TLSCAFile = strings.TrimSpace(r.FormValue("tls_ca_file"))
	if err := h.syslog.SaveClientConfig(current); err != nil {
		// Same reasoning as the server-side handler — TLS path
		// rejection is operator input.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.syslog.ApplyConfig(r.Context()); err != nil {
		log.Printf("syslog apply after save client: %v", err)
	}
	syslogResponse(w, r)
}

func (h *SyslogHandler) HandleAddFacility(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	if !allowedFacilities[name] {
		http.Error(w, "invalid facility", http.StatusBadRequest)
		return
	}
	if err := h.syslog.AddFacility(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.syslog.ApplyConfig(r.Context()); err != nil {
		log.Printf("syslog apply after add facility: %v", err)
	}
	syslogResponse(w, r)
}

func (h *SyslogHandler) HandleRemoveFacility(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}
	if err := h.syslog.RemoveFacility(idx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.syslog.ApplyConfig(r.Context()); err != nil {
		log.Printf("syslog apply after remove facility: %v", err)
	}
	syslogResponse(w, r)
}

func syslogResponse(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/syslog", http.StatusSeeOther)
}
