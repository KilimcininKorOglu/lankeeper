package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// MetricsHandler is a minimal pass-through that streams the
// snapshot in Prometheus exposition format. There is intentionally
// no template/i18n/auth path here: scrapers don't carry session
// cookies and they expect identical output across requests.
type MetricsHandler struct {
	metrics *services.MetricsService
}

func NewMetricsHandler(metrics *services.MetricsService) *MetricsHandler {
	return &MetricsHandler{metrics: metrics}
}

// HandleMetrics serves /metrics. The endpoint is wired through
// the LANOnly middleware in server.go so RemoteAddr filtering is
// already done by the time this runs. We set the spec content-
// type and stream directly into the ResponseWriter.
func (h *MetricsHandler) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	snap := h.metrics.Snapshot(r.Context())
	if err := snap.Write(w); err != nil {
		// At this point the headers + partial body have already
		// been flushed; we can't escalate to 500. Log and move on.
		log.Printf("metrics: write snapshot: %v", err)
	}
}
