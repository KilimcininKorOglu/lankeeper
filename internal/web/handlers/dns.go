package handlers

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

var dnsNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,251}[a-zA-Z0-9]$`)

type DNSHandler struct {
	renderer *tmpl.Renderer
	dns      *services.DNSService
	doh      *services.DoHService
}

func NewDNSHandler(renderer *tmpl.Renderer, dns *services.DNSService, doh *services.DoHService) *DNSHandler {
	return &DNSHandler{renderer: renderer, dns: dns, doh: doh}
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
			"DoHEnabled":    h.dns.GetDNSConfig().EnableDoH,
			"DoHUpstream":   h.dns.GetDNSConfig().DoHUpstream,
			"DoHResolvers":  services.BuiltInDoHResolvers(),
		},
	}

	if err := h.renderer.Render(w, "dns", "base", data); err != nil {
		log.Printf("render dns: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *DNSHandler) HandleClearLog(w http.ResponseWriter, r *http.Request) {
	if err := h.dns.ClearQueryLog(r.Context()); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
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
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dns", http.StatusSeeOther)
}

// HandleProbeDoT runs a one-shot connectivity check against the supplied
// DoT upstream and returns an inline HTMX-friendly status snippet (small
// HTML span) so the result lands in the Test button's hx-target.
func (h *DNSHandler) HandleProbeDoT(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	upstream := strings.TrimSpace(r.FormValue("dot_upstream"))
	if upstream == "" {
		clientError(w, r, http.StatusBadRequest, "error.upstreamRequired")
		return
	}
	latency, err := h.dns.ProbeDoT(r.Context(), upstream)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		_, _ = fmt.Fprintf(w, `<span class="badge badge-error">FAIL: %s</span>`, html.EscapeString(err.Error()))
		return
	}
	_, _ = fmt.Fprintf(w, `<span class="badge badge-success">OK (%dms)</span>`, latency.Milliseconds())
}

// HandleSaveDoT now drives the radio "encryption mode" with three
// values: "plain", "dot", "doh". Kept the historical handler name
// so the existing route stays stable; the form payload tells us
// which mode the operator picked.
func (h *DNSHandler) HandleSaveDoT(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	mode := r.FormValue("encryption_mode")
	dotUpstream := strings.TrimSpace(r.FormValue("dot_upstream"))
	dohUpstream := strings.TrimSpace(r.FormValue("doh_upstream"))

	var enableDoT, enableDoH bool
	switch mode {
	case "dot":
		if dotUpstream == "" {
			clientError(w, r, http.StatusBadRequest, "error.dotUpstreamRequired")
			return
		}
		enableDoT = true
	case "doh":
		if dohUpstream == "" {
			clientError(w, r, http.StatusBadRequest, "error.dohUpstreamRequired")
			return
		}
		if h.doh != nil {
			if err := h.doh.ValidateUpstream(dohUpstream); err != nil {
				fail(w, r, http.StatusBadRequest, err)
				return
			}
		}
		enableDoH = true
	case "plain", "":
		// both stay false
	default:
		clientError(w, r, http.StatusBadRequest, "error.unknownEncryptionMode")
		return
	}

	if err := h.dns.SaveDNSSettings(enableDoT, dotUpstream, enableDoH, dohUpstream); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	// Apply order depends on the direction, because the wrong one leaves
	// Unbound forwarding to a port nobody is listening on.
	//
	// Turning DoH on: dnscrypt-proxy must be up before Unbound reloads,
	// or Unbound's first forward query lands on a closed port and
	// triggers a 5-10s retry storm.
	//
	// Turning it off: Unbound must stop forwarding to 127.0.0.1:5353
	// first. Stopping dnscrypt-proxy while unbound.conf still names it
	// reproduces exactly the same failure on the way down, and the
	// settings are already persisted by this point, so the disabled
	// branch of the DoH service stops the daemon straight away.
	applyDNS := func() {
		if err := h.dns.ApplyConfig(r.Context()); err != nil {
			log.Printf("dns apply after mode change: %v", err)
		}
	}
	applyDoH := func() {
		if h.doh == nil {
			return
		}
		if err := h.doh.ApplyConfig(r.Context()); err != nil {
			log.Printf("doh apply after mode change: %v", err)
		}
	}

	applyDNSPlane(enableDoH, applyDoH, applyDNS)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dns", http.StatusSeeOther)
}

// HandleProbeDoH is the DoH counterpart to HandleProbeDoT. Catalogue
// names are not probable - the proxy resolver list maps name to
// endpoint. We hand the operator a clear error in that case rather
// than pretending to test something we can't reach.
func (h *DNSHandler) HandleProbeDoH(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	upstream := strings.TrimSpace(r.FormValue("doh_upstream"))
	if upstream == "" {
		clientError(w, r, http.StatusBadRequest, "error.upstreamRequired")
		return
	}
	if h.doh == nil {
		clientError(w, r, http.StatusServiceUnavailable, "error.dohUnavailable")
		return
	}
	latency, err := h.doh.Probe(r.Context(), upstream)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		_, _ = fmt.Fprintf(w, `<span class="badge badge-error">FAIL: %s</span>`, html.EscapeString(err.Error()))
		return
	}
	_, _ = fmt.Fprintf(w, `<span class="badge badge-success">OK (%dms)</span>`, latency.Milliseconds())
}

func (h *DNSHandler) HandleAddRecord(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	ip := strings.TrimSpace(r.FormValue("ip"))
	localZone := r.FormValue("local_zone") == "on"

	if !dnsNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidDNSName")
		return
	}
	if netutil.ValidateIP(ip) != nil {
		clientError(w, r, http.StatusBadRequest, "error.invalidIP")
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
		fail(w, r, http.StatusBadRequest, err)
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
		clientError(w, r, http.StatusBadRequest, "error.invalidIndex")
		return
	}
	if err := h.dns.RemoveStaticRecord(idx); err != nil {
		fail(w, r, http.StatusBadRequest, err)
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

// applyDNSPlane runs the two applies in the order the transition needs.
//
// Enabling DoH: the proxy comes up first, so Unbound never reloads with
// a forwarder that is not listening yet. Disabling it: Unbound drops the
// forwarder first, so the proxy is only stopped once nothing points at
// it. The handler used one fixed order for both, which was right on the
// way up and wrong on the way down.
func applyDNSPlane(enableDoH bool, applyDoH, applyDNS func()) {
	if enableDoH {
		applyDoH()
		applyDNS()
		return
	}
	applyDNS()
	applyDoH()
}
