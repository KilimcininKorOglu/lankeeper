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
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
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
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *NASHandler) HandleAddShare(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	name := r.FormValue("name")
	if name == "" {
		clientError(w, r, http.StatusBadRequest, "error.nameRequired")
		return
	}
	if len(name) > 64 || !nasNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidNameCharacters")
		return
	}

	rawPath := r.FormValue("path")
	if rawPath == "" {
		clientError(w, r, http.StatusBadRequest, "error.pathRequired")
		return
	}
	// Check the character set on the raw value, before Clean. The path is
	// rendered into an unquoted smb.conf directive, and Clean preserves
	// control characters, so a newline here would start a new directive
	// inside the share stanza.
	if netutil.ValidateFilesystemPath(rawPath) != nil {
		clientError(w, r, http.StatusBadRequest, "error.pathCharacters")
		return
	}

	path := filepath.Clean(rawPath)
	if !strings.HasPrefix(path, "/srv/") && !strings.HasPrefix(path, "/mnt/") {
		clientError(w, r, http.StatusBadRequest, "error.pathPrefix")
		return
	}

	share := config.ShareConfig{
		Name:     name,
		Path:     path,
		GuestOK:  r.FormValue("guestOk") == "true" || r.FormValue("guestOk") == "on",
		ReadOnly: r.FormValue("readOnly") == "true" || r.FormValue("readOnly") == "on",
	}

	if err := h.nas.AddShare(share); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
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
		fail(w, r, http.StatusBadRequest, err)
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
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	rawURL := r.FormValue("url")
	if rawURL == "" {
		clientError(w, r, http.StatusBadRequest, "error.urlRequired")
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		clientError(w, r, http.StatusBadRequest, "error.urlScheme")
		return
	}

	groups, err := h.nas.DiscoverM3UGroups(r.Context(), rawURL)
	if err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, g := range groups {
		escaped := html.EscapeString(g)
		_, _ = fmt.Fprintf(w, `<label style="display:flex;align-items:center;gap:var(--space-xs);cursor:pointer;padding:var(--space-xs) 0;"><input type="checkbox" name="includeGroups" value="%s" checked> %s</label>`, escaped, escaped)
	}
}
