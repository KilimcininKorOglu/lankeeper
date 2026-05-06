package handlers

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

var nasNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type NASHandler struct {
	renderer *tmpl.Renderer
	nas      *services.NASService
}

func NewNASHandler(renderer *tmpl.Renderer, nas *services.NASService) *NASHandler {
	return &NASHandler{renderer: renderer, nas: nas}
}

func (h *NASHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "nas",
		Data: map[string]any{
			"Shares":    h.nas.GetShares(),
			"M3UStatus": h.nas.GetM3UStatus(),
		},
	}

	if err := h.renderer.Render(w, "nas", "base", data); err != nil {
		log.Printf("render nas: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *NASHandler) HandleAddShare(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if len(name) > 64 || !nasNamePattern.MatchString(name) {
		http.Error(w, "name must be alphanumeric, dashes, or underscores (max 64 chars)", http.StatusBadRequest)
		return
	}

	path := filepath.Clean(r.FormValue("path"))
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(path, "/srv/") && !strings.HasPrefix(path, "/mnt/") {
		http.Error(w, "path must start with /srv/ or /mnt/", http.StatusBadRequest)
		return
	}

	share := config.ShareConfig{
		Name:     name,
		Path:     path,
		GuestOK:  r.FormValue("guestOk") == "true" || r.FormValue("guestOk") == "on",
		ReadOnly: r.FormValue("readOnly") == "true" || r.FormValue("readOnly") == "on",
	}

	if err := h.nas.AddShare(share); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if err := h.nas.ApplyConfig(r.Context()); err != nil {
		log.Printf("nas apply after add share: %v", err)
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/nas", http.StatusSeeOther)
}

func (h *NASHandler) HandleDeleteShare(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.nas.RemoveShare(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.nas.ApplyConfig(r.Context()); err != nil {
		log.Printf("nas apply after delete share: %v", err)
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/nas", http.StatusSeeOther)
}

func (h *NASHandler) HandleSyncM3U(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := h.nas.SyncM3U(r.Context()); err != nil {
			log.Printf("nas: m3u sync: %v", err)
		}
	}()

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "m3uSyncStarted")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/nas", http.StatusSeeOther)
}

func (h *NASHandler) HandleDiscoverGroups(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	rawURL := r.FormValue("url")
	if rawURL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.Error(w, "url must use http or https scheme", http.StatusBadRequest)
		return
	}

	groups, err := h.nas.DiscoverM3UGroups(r.Context(), rawURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, g := range groups {
		escaped := html.EscapeString(g)
		_, _ = fmt.Fprintf(w, `<label style="display:flex;align-items:center;gap:var(--space-xs);cursor:pointer;padding:var(--space-xs) 0;"><input type="checkbox" name="includeGroups" value="%s" checked> %s</label>`, escaped, escaped)
	}
}
