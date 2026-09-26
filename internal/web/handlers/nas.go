package handlers

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"time"

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
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *NASHandler) HandleAddShare(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	share, key := parseShareForm(r)
	if key != "" {
		clientError(w, r, http.StatusBadRequest, key)
		return
	}

	if err := h.nas.AddShare(share); err != nil {
		clientError(w, r, http.StatusInternalServerError, "error.saveFailed")
		return
	}
	if err := h.nas.ApplyConfig(r.Context()); err != nil {
		log.Printf("nas apply after add share: %v", err)
	}

	respondRefresh(w, r, "/nas")
}

// parseShareForm reads a share from the form. The second result is the
// locale key of the first invalid field, or "".
func parseShareForm(r *http.Request) (config.ShareConfig, string) {
	name := r.FormValue("name")
	rawPath := r.FormValue("path")
	if key := firstFailed(
		check{name == "", "error.nameRequired"},
		check{len(name) > 64 || !nasNamePattern.MatchString(name), "error.invalidNameCharacters"},
		check{rawPath == "", "error.pathRequired"},
	); key != "" {
		return config.ShareConfig{}, key
	}

	// One helper rather than a rule per call site. The order inside it
	// matters (character set on the raw value, prefix on the cleaned
	// one), and the M3U sync path proved that a reimplementation loses
	// a step.
	path, err := services.ValidateMediaPath(rawPath)
	if err != nil {
		return config.ShareConfig{}, mediaPathErrorKey(err)
	}

	return config.ShareConfig{
		Name:     name,
		Path:     path,
		GuestOK:  oneOf(r.FormValue("guestOk"), "true", "on"),
		ReadOnly: oneOf(r.FormValue("readOnly"), "true", "on"),
	}, ""
}

// mediaPathErrorKey maps a ValidateMediaPath error to its locale key. An
// error of any other kind is reported as a character problem.
func mediaPathErrorKey(err error) string {
	if errors.Is(err, services.ErrMediaPathPrefix) {
		return "error.pathPrefix"
	}
	return "error.pathCharacters"
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

	respondRefresh(w, r, "/nas")
}

// m3uSyncTimeout bounds a manual sync, which downloads every source.
const m3uSyncTimeout = 30 * time.Minute

// backgroundContext returns a context for work that continues after the
// handler returns. net/http cancels r.Context() at that moment, so work
// started on it fails at its first network call.
func backgroundContext(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), d)
}

func (h *NASHandler) HandleSyncM3U(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := backgroundContext(r, m3uSyncTimeout)
	go func() {
		defer cancel()
		if err := h.nas.SyncM3U(ctx); err != nil {
			log.Printf("nas: m3u sync: %v", err)
		}
	}()

	respondTrigger(w, r, "m3uSyncStarted", "/nas")
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
