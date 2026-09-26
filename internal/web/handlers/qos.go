package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

type QoSHandler struct {
	renderer *tmpl.Renderer
	qos      *services.QoSService
	cfg      *config.Config
}

func NewQoSHandler(renderer *tmpl.Renderer, qos *services.QoSService, cfg *config.Config) *QoSHandler {
	return &QoSHandler{renderer: renderer, qos: qos, cfg: cfg}
}

func (h *QoSHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	status, _ := h.qos.Status(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "qos",
		Data: map[string]any{
			"Status": status,
			"Config": h.cfg.QoS,
		},
	}

	if err := h.renderer.Render(w, "qos", "base", data); err != nil {
		log.Printf("render qos: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *QoSHandler) HandleApply(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}

	// The whole form is validated before the config is touched. The live
	// config is shared, so a field assigned before a later one failed
	// would stay in memory and reach disk on the next successful save.
	next, key := parseQoSForm(r, h.cfg.QoS)
	if key != "" {
		clientError(w, r, http.StatusBadRequest, key)
		return
	}
	h.cfg.QoS = next

	if err := h.cfg.SaveToFile(); err != nil {
		serverError(w, r, "error.saveFailed", err)
		return
	}

	if err := h.qos.Apply(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	respondRefresh(w, r, "/qos")
}

// maxQoSKbps is the largest bandwidth the form accepts, 10 Gbit/s.
const maxQoSKbps = 10000000

// parseQoSForm returns current updated with the submitted fields. An
// empty field keeps its stored value. The second result is the locale
// key of the first invalid field, or "", in which case current is
// returned unchanged.
func parseQoSForm(r *http.Request, current config.QoSConfig) (config.QoSConfig, string) {
	profile := r.FormValue("profile")
	cc := r.FormValue("congestionControl")
	upload, uploadOK := optionalKbps(r.FormValue("uploadKbps"), current.UploadKbps)
	download, downloadOK := optionalKbps(r.FormValue("downloadKbps"), current.DownloadKbps)
	if key := firstFailed(
		// The sets are the ones QoSService.Apply and config.Validate
		// accept, which are also the only values the page offers.
		check{!oneOf(profile, "", "cake", "fq_codel", "none"), "error.invalidQoSProfile"},
		check{!uploadOK, "error.invalidUploadBandwidth"},
		check{!downloadOK, "error.invalidDownloadBandwidth"},
		check{!oneOf(cc, "", "bbr", "cubic"), "error.invalidCongestionControl"},
	); key != "" {
		return current, key
	}

	next := current
	if profile != "" {
		next.Profile = profile
	}
	if cc != "" {
		next.CongestionControl = cc
	}
	next.UploadKbps = upload
	next.DownloadKbps = download
	next.Enabled = oneOf(r.FormValue("enabled"), "true", "on")
	return next, ""
}

// optionalKbps parses a bandwidth field, keeping current when it is
// empty.
func optionalKbps(raw string, current int) (int, bool) {
	if raw == "" {
		return current, true
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val < 0 || val > maxQoSKbps {
		return 0, false
	}
	return val, true
}

func (h *QoSHandler) HandleClear(w http.ResponseWriter, r *http.Request) {
	if err := h.qos.Clear(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	h.cfg.QoS.Enabled = false
	if err := h.cfg.SaveToFile(); err != nil {
		serverError(w, r, "error.saveFailed", err)
		return
	}

	respondRefresh(w, r, "/qos")
}
