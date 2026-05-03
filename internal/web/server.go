package web

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
	"github.com/KilimcininKorOglu/home-router/internal/web/handlers"
)

type Server struct {
	cfg      *config.Config
	auth     *Auth
	renderer *tmpl.Renderer
	loc      *i18n.I18n
	http     *http.Server
	network  *handlers.NetworkHandler
	firewall *handlers.FirewallHandler
	dns       *handlers.DNSHandler
	dhcp      *handlers.DHCPHandler
	dashboard *handlers.DashboardHandler
	settings  *handlers.SystemHandler
	qos       *handlers.QoSHandler
	vpn       *handlers.VPNHandler
	ovpn      *handlers.OpenVPNHandler
	routing   *handlers.RoutingHandler
	nas       *handlers.NASHandler
	storageh  *handlers.StorageHandler
	syslogh   *handlers.SyslogHandler
	ntph      *handlers.NTPHandler
	vlan      *handlers.VLANHandler
	pppoe     *handlers.PPPoEHandler
	health    *handlers.HealthCheckHandler
	sse       *SSEBroker
	monitor   *services.MonitorService
	dhcpSvc   *services.DHCPService
}

func NewServer(cfg *config.Config, loc *i18n.I18n, webFS fs.FS, updateSvc *services.UpdateService) (*Server, error) {
	auth := NewAuth(cfg.System.SessionSecret, cfg.System.AdminPasswordHash)

	renderer, err := tmpl.NewRenderer(webFS, loc)
	if err != nil {
		return nil, fmt.Errorf("init renderer: %w", err)
	}

	networkSvc := services.NewNetworkService(cfg)
	vlanHandler := handlers.NewVLANHandler(renderer, networkSvc, cfg)
	pppoeSvc := services.NewPPPoEService(cfg)
	usbSvc := services.NewUSBTetheringService(cfg)
	healthSvc := services.NewHealthCheckService(cfg)

	networkHandler := handlers.NewNetworkHandler(renderer, networkSvc, pppoeSvc, usbSvc, healthSvc)
	pppoeHandler := handlers.NewPPPoEHandler(renderer, pppoeSvc)
	healthHandler := handlers.NewHealthCheckHandler(renderer, healthSvc)

	nftTmpl, _ := fs.ReadFile(webFS, "../configs/sysconf/nftables.conf.tmpl")
	if nftTmpl == nil {
		nftTmpl = []byte("flush ruleset\n")
	}
	firewallSvc, err := services.NewFirewallServiceFromFS(cfg, string(nftTmpl))
	if err != nil {
		return nil, fmt.Errorf("init firewall service: %w", err)
	}
	firewallHandler := handlers.NewFirewallHandler(renderer, firewallSvc, cfg)

	dnsSvc := services.NewDNSService(cfg)
	dnsHandler := handlers.NewDNSHandler(renderer, dnsSvc)

	dhcpSvc := services.NewDHCPService(cfg)
	dhcpSvc.SetDNSService(dnsSvc)
	dhcpHandler := handlers.NewDHCPHandler(renderer, dhcpSvc)

	qosSvc := services.NewQoSService(cfg)
	qosHandler := handlers.NewQoSHandler(renderer, qosSvc, cfg)

	vpnSvc := services.NewVPNService(cfg)
	vpnHandler := handlers.NewVPNHandler(renderer, vpnSvc)

	ovpnSvc := services.NewOpenVPNService(cfg)
	ovpnHandler := handlers.NewOpenVPNHandler(renderer, ovpnSvc)

	routingSvc := services.NewRoutingService(cfg)
	routingHandler := handlers.NewRoutingHandler(renderer, routingSvc)

	nasSvc := services.NewNASService(cfg)
	nasHandler := handlers.NewNASHandler(renderer, nasSvc)

	storageSvc := services.NewStorageService(cfg)
	storageHandler := handlers.NewStorageHandler(renderer, storageSvc)

	syslogSvc := services.NewSyslogService(cfg)
	syslogHandler := handlers.NewSyslogHandler(renderer, syslogSvc)

	ntpSvc := services.NewNTPService(cfg)
	ntpHandler := handlers.NewNTPHandler(renderer, ntpSvc)

	backupSvc := services.NewBackupService("/etc/home-router")
	monitorSvc := services.NewMonitorService()
	dashboardHandler := handlers.NewDashboardHandler(renderer, monitorSvc, pppoeSvc, dhcpSvc)
	settingsHandler := handlers.NewSystemHandler(renderer, cfg, loc, dhcpSvc, backupSvc, updateSvc)
	sseBroker := NewSSEBroker()

	s := &Server{
		cfg:       cfg,
		auth:      auth,
		renderer:  renderer,
		loc:       loc,
		network:   networkHandler,
		firewall:  firewallHandler,
		dns:       dnsHandler,
		dhcp:      dhcpHandler,
		dhcpSvc:   dhcpSvc,
		dashboard: dashboardHandler,
		settings:  settingsHandler,
		qos:       qosHandler,
		vpn:       vpnHandler,
		ovpn:      ovpnHandler,
		routing:   routingHandler,
		nas:       nasHandler,
		vlan:      vlanHandler,
		pppoe:     pppoeHandler,
		health:    healthHandler,
		storageh:  storageHandler,
		syslogh:   syslogHandler,
		ntph:      ntpHandler,
		sse:       sseBroker,
		monitor:   monitorSvc,
	}

	mux := http.NewServeMux()
	s.routes(mux, webFS)

	var handler http.Handler = mux

	_, lanNet, _ := net.ParseCIDR("10.10.10.0/24")
	allowedNets := []*net.IPNet{lanNet}
	for _, iface := range cfg.Interfaces {
		if iface.Role == "lan" && iface.Address != "" {
			_, n, err := net.ParseCIDR(iface.Address)
			if err == nil {
				allowedNets = append(allowedNets, n)
			}
		}
	}

	handler = RequestLogger(handler)
	handler = SecurityHeaders(handler)
	handler = CSRFProtect(handler)
	rateLimiter := NewRateLimiter(30, 60)
	handler = rateLimiter.Middleware(handler)
	handler = i18n.Middleware(loc)(handler)
	handler = LANOnly(allowedNets)(handler)

	addr := fmt.Sprintf("%s:%d", cfg.System.WebBind, cfg.System.WebPort)
	s.http = &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	return s, nil
}

func (s *Server) Serve(ctx context.Context) error {
	var ifaceNames []string
	for _, iface := range s.cfg.Interfaces {
		ifaceNames = append(ifaceNames, iface.Device)
	}

	stopMonitor := make(chan struct{})
	go s.monitor.Start(stopMonitor, ifaceNames)
	go s.publishStats(ctx)

	go func() {
		<-ctx.Done()
		close(stopMonitor)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.http.Shutdown(shutdownCtx)
	}()

	log.Printf("web server listening on %s (TLS mode: %s)", s.http.Addr, s.cfg.System.TLS.Mode)

	dataDir := "/var/lib/home-router"
	certInfo, err := config.EnsureTLSCert(&s.cfg.System.TLS, dataDir)
	if err != nil {
		return fmt.Errorf("ensure TLS cert: %w", err)
	}
	log.Printf("TLS certificate ready: %s (expires: %s)", certInfo.Issuer, certInfo.NotAfter)

	certFile := s.cfg.System.TLS.CertFile
	keyFile := s.cfg.System.TLS.KeyFile
	if certFile == "" {
		certFile = "/var/lib/home-router/tls/server.crt"
	}
	if keyFile == "" {
		keyFile = "/var/lib/home-router/tls/server.key"
	}

	// Retro-mirror DHCP static leases to DNS so hosts added in older
	// versions (or via direct router.yaml edits) become resolvable on
	// every restart. Idempotent — Sync rebuilds the dhcp-static set.
	if s.dhcpSvc != nil {
		if err := s.dhcpSvc.SyncStaticDNSRecords(ctx); err != nil {
			log.Printf("startup: dhcp dns sync failed: %v", err)
		} else {
			log.Printf("startup: dhcp static lease DNS records synced")
		}
	}

	err = s.http.ListenAndServeTLS(certFile, keyFile)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) routes(mux *http.ServeMux, webFS fs.FS) {
	staticFS, _ := fs.Sub(webFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	loginLimiter := NewRateLimiter(1, 5)
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.Handle("POST /login", loginLimiter.Middleware(http.HandlerFunc(s.handleLogin)))
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("POST /settings/lang", s.handleLangSwitch)

	authed := AuthRequired(s.auth)
	mux.Handle("GET /{$}", authed(http.HandlerFunc(s.dashboard.HandlePage)))
	mux.Handle("GET /events/stats", authed(http.HandlerFunc(s.sse.ServeHTTP)))
	mux.Handle("GET /settings", authed(http.HandlerFunc(s.settings.HandleSettingsPage)))
	mux.Handle("POST /settings/web-password", authed(http.HandlerFunc(s.settings.HandleChangeWebPassword)))
	mux.Handle("POST /settings/root-password", authed(http.HandlerFunc(s.settings.HandleChangeRootPassword)))
	mux.Handle("POST /settings/hostname", authed(http.HandlerFunc(s.settings.HandleUpdateHostname)))
	mux.Handle("POST /settings/timezone", authed(http.HandlerFunc(s.settings.HandleUpdateTimezone)))
	mux.Handle("POST /system/reboot", authed(http.HandlerFunc(s.settings.HandleReboot)))
	mux.Handle("POST /system/factory-reset", authed(http.HandlerFunc(s.settings.HandleFactoryReset)))
	mux.Handle("GET /system/backup/export", authed(http.HandlerFunc(s.settings.HandleExport)))
	mux.Handle("POST /system/backup/import", authed(http.HandlerFunc(s.settings.HandleImport)))
	mux.Handle("GET /system/update/check", authed(http.HandlerFunc(s.settings.HandleCheckUpdate)))
	mux.Handle("POST /system/update/apply", authed(http.HandlerFunc(s.settings.HandleApplyUpdate)))
	mux.Handle("POST /system/update/confirm", authed(http.HandlerFunc(s.settings.HandleConfirmUpdate)))
	mux.Handle("POST /system/update/rollback", authed(http.HandlerFunc(s.settings.HandleRollbackUpdate)))
	mux.Handle("GET /api/version", http.HandlerFunc(s.settings.HandleVersion))
	mux.Handle("GET /network", authed(http.HandlerFunc(s.network.HandlePage)))
	mux.Handle("POST /network/pppoe/connect", authed(http.HandlerFunc(s.pppoe.HandleConnect)))
	mux.Handle("POST /network/pppoe/disconnect", authed(http.HandlerFunc(s.pppoe.HandleDisconnect)))
	mux.Handle("POST /network/pppoe/sniff/start", authed(http.HandlerFunc(s.pppoe.HandleSniffStart)))
	mux.Handle("POST /network/pppoe/sniff/stop", authed(http.HandlerFunc(s.pppoe.HandleSniffStop)))
	mux.Handle("POST /network/healthcheck/{name}/reset", authed(http.HandlerFunc(s.health.HandleReset)))
	mux.Handle("POST /network/vlan", authed(http.HandlerFunc(s.vlan.HandleAdd)))
	mux.Handle("DELETE /network/vlan/{id}", authed(http.HandlerFunc(s.vlan.HandleDelete)))
	mux.Handle("GET /firewall", authed(http.HandlerFunc(s.firewall.HandlePage)))
	mux.Handle("POST /firewall/apply", authed(http.HandlerFunc(s.firewall.HandleApply)))
	mux.Handle("POST /firewall/confirm", authed(http.HandlerFunc(s.firewall.HandleConfirm)))
	mux.Handle("POST /firewall/rollback", authed(http.HandlerFunc(s.firewall.HandleRollback)))
	mux.Handle("POST /firewall/port-forwards", authed(http.HandlerFunc(s.firewall.HandleAddPortForward)))
	mux.Handle("DELETE /firewall/port-forwards/{index}", authed(http.HandlerFunc(s.firewall.HandleDeletePortForward)))
	mux.Handle("POST /firewall/open-ports", authed(http.HandlerFunc(s.firewall.HandleAddOpenPort)))
	mux.Handle("DELETE /firewall/open-ports/{index}", authed(http.HandlerFunc(s.firewall.HandleDeleteOpenPort)))
	mux.Handle("POST /firewall/open-ports/{index}/toggle", authed(http.HandlerFunc(s.firewall.HandleToggleOpenPort)))
	mux.Handle("POST /firewall/rules", authed(http.HandlerFunc(s.firewall.HandleAddRule)))
	mux.Handle("DELETE /firewall/rules/{index}", authed(http.HandlerFunc(s.firewall.HandleDeleteRule)))
	mux.Handle("POST /firewall/rules/{index}/toggle", authed(http.HandlerFunc(s.firewall.HandleToggleRule)))
	mux.Handle("GET /dns", authed(http.HandlerFunc(s.dns.HandlePage)))
	mux.Handle("POST /dns/clear-log", authed(http.HandlerFunc(s.dns.HandleClearLog)))
	mux.Handle("POST /dns/blocklist/update", authed(http.HandlerFunc(s.dns.HandleUpdateBlocklist)))
	mux.Handle("POST /dns/dot", authed(http.HandlerFunc(s.dns.HandleSaveDoT)))
	mux.Handle("POST /dns/records", authed(http.HandlerFunc(s.dns.HandleAddRecord)))
	mux.Handle("DELETE /dns/records/{index}", authed(http.HandlerFunc(s.dns.HandleDeleteRecord)))
	mux.Handle("GET /dhcp", authed(http.HandlerFunc(s.dhcp.HandlePage)))
	mux.Handle("POST /dhcp/static", authed(http.HandlerFunc(s.dhcp.HandleAddStatic)))
	mux.Handle("DELETE /dhcp/static/{index}", authed(http.HandlerFunc(s.dhcp.HandleDeleteStatic)))
	mux.Handle("GET /qos", authed(http.HandlerFunc(s.qos.HandlePage)))
	mux.Handle("POST /qos/apply", authed(http.HandlerFunc(s.qos.HandleApply)))
	mux.Handle("POST /qos/clear", authed(http.HandlerFunc(s.qos.HandleClear)))
	mux.Handle("GET /vpn", authed(http.HandlerFunc(s.vpn.HandlePage)))
	mux.Handle("POST /vpn/server/peer", authed(http.HandlerFunc(s.vpn.HandleAddPeer)))
	mux.Handle("DELETE /vpn/server/peer/{name}", authed(http.HandlerFunc(s.vpn.HandleRemovePeer)))
	mux.Handle("POST /vpn/server/start", authed(http.HandlerFunc(s.vpn.HandleServerStart)))
	mux.Handle("POST /vpn/server/stop", authed(http.HandlerFunc(s.vpn.HandleServerStop)))
	mux.Handle("POST /vpn/client/{name}/connect", authed(http.HandlerFunc(s.vpn.HandleConnectClient)))
	mux.Handle("POST /vpn/client/{name}/disconnect", authed(http.HandlerFunc(s.vpn.HandleDisconnectClient)))
	mux.Handle("GET /openvpn", authed(http.HandlerFunc(s.ovpn.HandlePage)))
	mux.Handle("POST /openvpn/init-pki", authed(http.HandlerFunc(s.ovpn.HandleInitPKI)))
	mux.Handle("POST /openvpn/server/start", authed(http.HandlerFunc(s.ovpn.HandleServerStart)))
	mux.Handle("POST /openvpn/server/stop", authed(http.HandlerFunc(s.ovpn.HandleServerStop)))
	mux.Handle("POST /openvpn/server/client", authed(http.HandlerFunc(s.ovpn.HandleAddClient)))
	mux.Handle("DELETE /openvpn/server/client/{name}", authed(http.HandlerFunc(s.ovpn.HandleRevokeClient)))
	mux.Handle("GET /openvpn/server/client/{name}/config", authed(http.HandlerFunc(s.ovpn.HandleDownloadOVPN)))
	mux.Handle("POST /openvpn/client", authed(http.HandlerFunc(s.ovpn.HandleAddOutboundClient)))
	mux.Handle("POST /openvpn/client/{name}/connect", authed(http.HandlerFunc(s.ovpn.HandleConnectOutbound)))
	mux.Handle("POST /openvpn/client/{name}/disconnect", authed(http.HandlerFunc(s.ovpn.HandleDisconnectOutbound)))
	mux.Handle("GET /routing", authed(http.HandlerFunc(s.routing.HandlePage)))
	mux.Handle("POST /routing/policy", authed(http.HandlerFunc(s.routing.HandleAddPolicy)))
	mux.Handle("DELETE /routing/policy/{name}", authed(http.HandlerFunc(s.routing.HandleDeletePolicy)))
	mux.Handle("POST /routing/reorder", authed(http.HandlerFunc(s.routing.HandleReorder)))
	mux.Handle("GET /nas", authed(http.HandlerFunc(s.nas.HandlePage)))
	mux.Handle("POST /nas/shares", authed(http.HandlerFunc(s.nas.HandleAddShare)))
	mux.Handle("DELETE /nas/shares/{name}", authed(http.HandlerFunc(s.nas.HandleDeleteShare)))
	mux.Handle("POST /nas/m3u/sync", authed(http.HandlerFunc(s.nas.HandleSyncM3U)))
	mux.Handle("POST /nas/m3u/discover-groups", authed(http.HandlerFunc(s.nas.HandleDiscoverGroups)))
	mux.Handle("GET /storage", authed(http.HandlerFunc(s.storageh.HandlePage)))
	mux.Handle("GET /syslog", authed(http.HandlerFunc(s.syslogh.HandlePage)))
	mux.Handle("POST /syslog/server", authed(http.HandlerFunc(s.syslogh.HandleSaveServerConfig)))
	mux.Handle("POST /syslog/client", authed(http.HandlerFunc(s.syslogh.HandleSaveClientConfig)))
	mux.Handle("POST /syslog/client/facilities", authed(http.HandlerFunc(s.syslogh.HandleAddFacility)))
	mux.Handle("DELETE /syslog/client/facilities/{index}", authed(http.HandlerFunc(s.syslogh.HandleRemoveFacility)))
	mux.Handle("GET /ntp", authed(http.HandlerFunc(s.ntph.HandlePage)))
	mux.Handle("POST /ntp/force-sync", authed(http.HandlerFunc(s.ntph.HandleForceSync)))
	mux.Handle("POST /ntp/sources", authed(http.HandlerFunc(s.ntph.HandleAddSource)))
	mux.Handle("DELETE /ntp/sources/{index}", authed(http.HandlerFunc(s.ntph.HandleRemoveSource)))
	mux.Handle("POST /ntp/allow", authed(http.HandlerFunc(s.ntph.HandleAddAllowSubnet)))
	mux.Handle("DELETE /ntp/allow/{index}", authed(http.HandlerFunc(s.ntph.HandleRemoveAllowSubnet)))
	mux.Handle("POST /ntp/settings", authed(http.HandlerFunc(s.ntph.HandleSaveSettings)))
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.auth.IsAuthenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	lang := i18n.LangFromContext(r.Context())
	data := &tmpl.PageData{
		Lang:      lang,
		Page:      "login",
		CSRFToken: getOrCreateCSRFToken(w, r),
	}
	if err := s.renderer.Render(w, "login", "auth", data); err != nil {
		log.Printf("render login: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())
	password := r.FormValue("password")

	if !s.auth.VerifyPassword(password) {
		data := &tmpl.PageData{
			Lang:      lang,
			Page:      "login",
			CSRFToken: getOrCreateCSRFToken(w, r),
			Error:     s.loc.T(lang, "auth.wrongPassword"),
		}
		w.WriteHeader(http.StatusUnauthorized)
		if err := s.renderer.Render(w, "login", "auth", data); err != nil {
			log.Printf("render login error: %v", err)
		}
		return
	}

	if err := s.auth.Login(w, r); err != nil {
		log.Printf("login session error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.Logout(w, r)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleLangSwitch(w http.ResponseWriter, r *http.Request) {
	lang := r.FormValue("lang")
	if !s.loc.HasLocale(lang) {
		lang = s.loc.Fallback()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "lang",
		Value:    lang,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) publishStats(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.sse.ClientCount() > 0 {
				stats := s.monitor.GetCurrent()
				s.sse.Publish("stats", stats)
			}
		}
	}
}
