package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

var vpnNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type VPNHandler struct {
	renderer *tmpl.Renderer
	vpn      *services.VPNService
}

func NewVPNHandler(renderer *tmpl.Renderer, vpn *services.VPNService) *VPNHandler {
	return &VPNHandler{renderer: renderer, vpn: vpn}
}

func (h *VPNHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	tunnels, _ := h.vpn.ListClientTunnels(r.Context())
	serverStatus, _ := h.vpn.ServerStatus(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "vpn",
		Data: map[string]any{
			"Tunnels": tunnels,
			"Server":  serverStatus,
		},
	}

	if err := h.renderer.Render(w, "vpn", "base", data); err != nil {
		log.Printf("render vpn: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

func (h *VPNHandler) HandleAddPeer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	name := r.FormValue("name")
	if name == "" {
		clientError(w, r, http.StatusBadRequest, "error.nameRequired")
		return
	}
	if len(name) > 64 || !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidNameCharacters")
		return
	}

	peerType := r.FormValue("peerType")
	siteToSite := peerType == "site-to-site"
	endpoint := r.FormValue("endpoint")
	if endpoint != "" && !strings.Contains(endpoint, ":") {
		clientError(w, r, http.StatusBadRequest, "error.endpointFormat")
		return
	}

	var remoteSubnets []string
	if raw := strings.TrimSpace(r.FormValue("remoteSubnets")); raw != "" && siteToSite {
		for s := range strings.SplitSeq(raw, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				if err := netutil.ValidateCIDR(trimmed); err != nil {
					clientErrorf(w, r, http.StatusBadRequest, "error.invalidRemoteSubnet", trimmed)
					return
				}
				remoteSubnets = append(remoteSubnets, trimmed)
			}
		}
	}

	peer, privKey, err := h.vpn.AddPeer(r.Context(), name, siteToSite, remoteSubnets, endpoint)
	switch {
	case errors.Is(err, services.ErrPeerSubnetConflict):
		clientError(w, r, http.StatusBadRequest, "error.peerSubnetConflict")
		return
	case errors.Is(err, services.ErrPeerNameInUse):
		clientError(w, r, http.StatusBadRequest, "error.duplicateName")
		return
	case err != nil:
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	confStr := h.vpn.GeneratePeerConfig(peer, privKey)

	setSecretDownloadHeaders(w, "text/plain", name+".conf")
	// Not an HTML context: attachment disposition, text/plain,
	// and a global nosniff header.
	// #nosec G705
	_, _ = w.Write([]byte(confStr))
}

// HandleDownloadPeerConfig re-issues a peer's client configuration.
//
// The config carries the peer's private key, so it goes out with the
// same headers as the OpenVPN profile and the backup archive: an
// attachment disposition and no-store, because Content-Disposition
// alone says how to present the body, not whether to keep it.
func (h *VPNHandler) HandleDownloadPeerConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if len(name) > 64 || !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidPeerName")
		return
	}

	conf, err := h.vpn.PeerConfig(name)
	switch {
	case errors.Is(err, services.ErrPeerNotFound):
		clientError(w, r, http.StatusNotFound, "error.peerNotFound")
		return
	case errors.Is(err, services.ErrPeerKeyUnavailable):
		// Not a fault: the peer works, it just predates key storage or
		// lost its key with the credential key. Saying so is the only
		// way the operator learns replacing it is the way forward.
		clientError(w, r, http.StatusGone, "error.peerKeyUnavailable")
		return
	case err != nil:
		fail(w, r, http.StatusInternalServerError, err)
		return
	}

	setSecretDownloadHeaders(w, "text/plain", name+".conf")
	// Not an HTML context: attachment disposition, text/plain, and a
	// global nosniff header.
	// #nosec G705
	_, _ = w.Write([]byte(conf))
}

func (h *VPNHandler) HandleRemovePeer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.vpn.RemovePeer(name); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	respondRefresh(w, r, "/vpn")
}

func (h *VPNHandler) HandleServerStart(w http.ResponseWriter, r *http.Request) {
	// "Already running" is treated as a no-op so a double-click in
	// the UI is benign rather than throwing a 500. The mutex inside
	// ServerUp serialises concurrent requests; the second one finds
	// `running == true` and returns ErrVPNAlreadyRunning.
	if err := h.vpn.ServerUp(r.Context()); err != nil && !errors.Is(err, services.ErrVPNAlreadyRunning) {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/vpn")
}

func (h *VPNHandler) HandleServerStop(w http.ResponseWriter, r *http.Request) {
	if err := h.vpn.ServerDown(r.Context()); err != nil && !errors.Is(err, services.ErrVPNAlreadyStopped) {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/vpn")
}

func (h *VPNHandler) HandleConnectClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidClientName")
		return
	}
	if err := h.vpn.ConnectClient(r.Context(), name); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/vpn")
}

// --- Site-to-site wizard ---

// HandleS2SWizardPage renders the multi-step wizard for issuing a
// new site-to-site invite. Step navigation is client-side; the
// server sees one request per action (create invite, finalize ack,
// cancel pending).
func (h *VPNHandler) HandleS2SWizardPage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())
	serverStatus, _ := h.vpn.ServerStatus(r.Context())

	data := &tmpl.PageData{
		Lang: lang,
		Page: "vpn-s2s",
		Data: map[string]any{
			"Server": serverStatus,
		},
	}
	if err := h.renderer.Render(w, "vpn-s2s", "base", data); err != nil {
		log.Printf("render vpn-s2s: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}

// HandleS2SInvite issues a new pending peer + token on the
// originating router. Returns JSON {"token": "...", "peerName": "..."}.
func (h *VPNHandler) HandleS2SInvite(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	name := r.FormValue("name")
	siteName := r.FormValue("siteName")
	endpoint := r.FormValue("endpoint")
	remoteRaw := strings.TrimSpace(r.FormValue("remoteSubnets"))

	if name == "" || !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidName")
		return
	}
	if endpoint == "" || !strings.Contains(endpoint, ":") {
		clientError(w, r, http.StatusBadRequest, "error.endpointFormat")
		return
	}
	var remote []string
	for s := range strings.SplitSeq(remoteRaw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if err := netutil.ValidateCIDR(s); err != nil {
			clientErrorf(w, r, http.StatusBadRequest, "error.invalidCIDR", s)
			return
		}
		remote = append(remote, s)
	}
	if len(remote) == 0 {
		clientError(w, r, http.StatusBadRequest, "error.remoteSubnetRequired")
		return
	}

	token, peer, err := h.vpn.CreateS2SInvite(r.Context(), name, siteName, endpoint, remote)
	if err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"token":` + jsonString(token) +
		`,"peerName":` + jsonString(peer.Name) +
		`,"expiresAt":` + jsonString(peer.InviteExpiresAt.Format("2006-01-02T15:04:05Z07:00")) +
		`}`))
}

// HandleS2SJoin parses an incoming invite token, registers the
// remote side as a peer on this router, and returns the ack token
// + the local public key so the operator can paste it back.
func (h *VPNHandler) HandleS2SJoin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		clientError(w, r, http.StatusBadRequest, "error.tokenRequired")
		return
	}
	ack, pub, peer, err := h.vpn.ConsumeInvite(r.Context(), token)
	if err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ackToken":` + jsonString(ack) +
		`,"publicKey":` + jsonString(pub) +
		`,"peerName":` + jsonString(peer.Name) +
		`}`))
}

// HandleS2SFinalize completes the wizard on the originating side
// by accepting the ack token from the joining router.
func (h *VPNHandler) HandleS2SFinalize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		clientError(w, r, http.StatusBadRequest, "error.badForm")
		return
	}
	name := r.FormValue("peerName")
	ack := strings.TrimSpace(r.FormValue("ackToken"))
	if name == "" || !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidPeerName")
		return
	}
	if ack == "" {
		clientError(w, r, http.StatusBadRequest, "error.ackTokenRequired")
		return
	}
	if _, err := h.vpn.FinalizeInvite(r.Context(), name, ack); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	// Best-effort live reload of wgs0; if syncconf fails (e.g. the
	// server isn't running yet) we let the operator decide whether
	// to bring the tunnel up manually from the main /vpn page.
	if err := h.vpn.SyncWGServer(r.Context()); err != nil {
		log.Printf("vpn s2s syncconf: %v", err)
	}
	respondRefresh(w, r, "/vpn")
}

// HandleS2SCancel aborts a pending invite.
func (h *VPNHandler) HandleS2SCancel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidName")
		return
	}
	if err := h.vpn.CancelInvite(name); err != nil {
		fail(w, r, http.StatusBadRequest, err)
		return
	}
	respondRefresh(w, r, "/vpn/s2s")
}

// HandleS2SRotateKey replaces the token signing key. This is the
// operator's response to a suspected disclosure, and also the way to
// revoke invite tokens that were handed out and should not be redeemed:
// every outstanding token stops verifying at once. Established tunnels
// authenticate with WireGuard keys and are unaffected.
func (h *VPNHandler) HandleS2SRotateKey(w http.ResponseWriter, r *http.Request) {
	if err := h.vpn.RotateS2SSigningKey(); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/vpn/s2s")
}

// HandleS2SHealth returns the current handshake/transfer state
// for one site-to-site peer.
func (h *VPNHandler) HandleS2SHealth(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidName")
		return
	}
	info, err := h.vpn.S2SHealth(r.Context(), name)
	if err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	body := `{"name":` + jsonString(info.Name) +
		`,"online":` + boolStr(info.Online) +
		`,"handshakeAgeSec":` + intStr(info.HandshakeAgeSec) +
		`,"rxBytes":` + uintStr(info.RxBytes) +
		`,"txBytes":` + uintStr(info.TxBytes) +
		`}`
	_, _ = w.Write([]byte(body))
}

// HandleS2SReachability fires a single ping over wgs0 to test
// the remote LAN gateway. Returns 204 on success, 502 on failure.
func (h *VPNHandler) HandleS2SReachability(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidName")
		return
	}
	if err := h.vpn.S2SReachability(r.Context(), name); err != nil {
		fail(w, r, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// jsonString escapes an arbitrary Go string for safe embedding in
// the manually-rendered JSON responses above. We use this rather
// than encoding/json so the responses stay zero-allocation on the
// hot path; the strings involved are operator-supplied identifiers
// and base64 token payloads, so the escape set is minimal.
func jsonString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				_, _ = fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func intStr(i int64) string   { return fmt.Sprintf("%d", i) }
func uintStr(u uint64) string { return fmt.Sprintf("%d", u) }

func (h *VPNHandler) HandleDisconnectClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !vpnNamePattern.MatchString(name) {
		clientError(w, r, http.StatusBadRequest, "error.invalidClientName")
		return
	}
	if err := h.vpn.DisconnectClient(r.Context(), name); err != nil {
		fail(w, r, http.StatusInternalServerError, err)
		return
	}
	respondRefresh(w, r, "/vpn")
}
