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

	"github.com/KilimcininKorOglu/home-router/internal/agent"
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
	agent    *agent.Client
	http     *http.Server
	network  *handlers.NetworkHandler
	firewall *handlers.FirewallHandler
	dns       *handlers.DNSHandler
	dhcp      *handlers.DHCPHandler
	dashboard *handlers.DashboardHandler
	settings  *handlers.SystemHandler
	qos       *handlers.QoSHandler
	sse       *SSEBroker
	monitor   *services.MonitorService
}

func NewServer(cfg *config.Config, loc *i18n.I18n, agentClient *agent.Client, webFS fs.FS) (*Server, error) {
	auth := NewAuth(cfg.System.SessionSecret, cfg.System.AdminPasswordHash)

	renderer, err := tmpl.NewRenderer(webFS, loc)
	if err != nil {
		return nil, fmt.Errorf("init renderer: %w", err)
	}

	networkSvc := services.NewNetworkService(cfg)
	pppoeSvc := services.NewPPPoEService(cfg)
	usbSvc := services.NewUSBTetheringService(cfg)
	healthSvc := services.NewHealthCheckService(cfg)

	networkHandler := handlers.NewNetworkHandler(renderer, networkSvc, pppoeSvc, usbSvc, healthSvc)

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
	dhcpHandler := handlers.NewDHCPHandler(renderer, dhcpSvc)

	qosSvc := services.NewQoSService(cfg)
	qosHandler := handlers.NewQoSHandler(renderer, qosSvc, cfg)

	monitorSvc := services.NewMonitorService()
	dashboardHandler := handlers.NewDashboardHandler(renderer, monitorSvc, pppoeSvc, dhcpSvc)
	settingsHandler := handlers.NewSystemHandler(renderer, cfg)
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
		dashboard: dashboardHandler,
		settings:  settingsHandler,
		qos:       qosHandler,
		sse:       sseBroker,
		monitor:   monitorSvc,
		agent:     agentClient,
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

	certFile := s.cfg.System.TLS.CertFile
	keyFile := s.cfg.System.TLS.KeyFile
	if certFile == "" {
		certFile = "/var/lib/home-router/tls/server.crt"
	}
	if keyFile == "" {
		keyFile = "/var/lib/home-router/tls/server.key"
	}

	err := s.http.ListenAndServeTLS(certFile, keyFile)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) routes(mux *http.ServeMux, webFS fs.FS) {
	staticFS, _ := fs.Sub(webFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("POST /settings/lang", s.handleLangSwitch)

	authed := AuthRequired(s.auth)
	mux.Handle("GET /{$}", authed(http.HandlerFunc(s.dashboard.HandlePage)))
	mux.Handle("GET /events/stats", authed(http.HandlerFunc(s.sse.ServeHTTP)))
	mux.Handle("GET /settings", authed(http.HandlerFunc(s.settings.HandleSettingsPage)))
	mux.Handle("POST /settings/password", authed(http.HandlerFunc(s.settings.HandleChangePassword)))
	mux.Handle("GET /network", authed(http.HandlerFunc(s.network.HandlePage)))
	mux.Handle("GET /firewall", authed(http.HandlerFunc(s.firewall.HandlePage)))
	mux.Handle("POST /firewall/apply", authed(http.HandlerFunc(s.firewall.HandleApply)))
	mux.Handle("POST /firewall/confirm", authed(http.HandlerFunc(s.firewall.HandleConfirm)))
	mux.Handle("POST /firewall/rollback", authed(http.HandlerFunc(s.firewall.HandleRollback)))
	mux.Handle("POST /firewall/port-forwards", authed(http.HandlerFunc(s.firewall.HandleAddPortForward)))
	mux.Handle("DELETE /firewall/port-forwards/{index}", authed(http.HandlerFunc(s.firewall.HandleDeletePortForward)))
	mux.Handle("GET /dns", authed(http.HandlerFunc(s.dns.HandlePage)))
	mux.Handle("POST /dns/clear-log", authed(http.HandlerFunc(s.dns.HandleClearLog)))
	mux.Handle("GET /dhcp", authed(http.HandlerFunc(s.dhcp.HandlePage)))
	mux.Handle("POST /dhcp/static", authed(http.HandlerFunc(s.dhcp.HandleAddStatic)))
	mux.Handle("GET /qos", authed(http.HandlerFunc(s.qos.HandlePage)))
	mux.Handle("POST /qos/apply", authed(http.HandlerFunc(s.qos.HandleApply)))
	mux.Handle("POST /qos/clear", authed(http.HandlerFunc(s.qos.HandleClear)))
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

	http.Redirect(w, r, r.Referer(), http.StatusSeeOther)
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
