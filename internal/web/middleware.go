package web

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

func AuthRequired(auth *Auth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !auth.IsAuthenticated(r) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func CSRFProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			token := getOrCreateCSRFToken(w, r)
			w.Header().Set("X-CSRF-Token", token)
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie("csrf_token")
		if err != nil {
			httpErrorT(w, r, http.StatusForbidden, "error.csrfMissing")
			return
		}

		header := r.Header.Get("X-CSRF-Token")
		formVal := r.FormValue("csrf_token")
		token := header
		if token == "" {
			token = formVal
		}

		if token == "" || token != cookie.Value {
			httpErrorT(w, r, http.StatusForbidden, "error.csrfInvalid")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func getOrCreateCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("csrf_token"); err == nil {
		return c.Value
	}

	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	return token
}

func LANOnly(allowedNets []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}

			ip := net.ParseIP(host)
			if ip == nil {
				httpErrorT(w, r, http.StatusForbidden, "error.forbidden")
				return
			}

			if ip.IsLoopback() {
				next.ServeHTTP(w, r)
				return
			}

			for _, n := range allowedNets {
				if n.Contains(ip) {
					next.ServeHTTP(w, r)
					return
				}
			}

			httpErrorT(w, r, http.StatusForbidden, "error.forbidden")
		})
	}
}

type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]*tokenBucket
	rate    int
	burst   int
}

type tokenBucket struct {
	tokens    float64
	lastCheck time.Time
}

func NewRateLimiter(rate, burst int) *RateLimiter {
	rl := &RateLimiter{
		clients: make(map[string]*tokenBucket),
		rate:    rate,
		burst:   burst,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, b := range rl.clients {
			if now.Sub(b.lastCheck) > 5*time.Minute {
				delete(rl.clients, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.clients[ip]
	if !ok {
		rl.clients[ip] = &tokenBucket{
			tokens:    float64(rl.burst) - 1,
			lastCheck: now,
		}
		return true
	}

	elapsed := now.Sub(b.lastCheck).Seconds()
	b.tokens += elapsed * float64(rl.rate)
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastCheck = now

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}

		if !rl.Allow(host) {
			httpErrorT(w, r, http.StatusTooManyRequests, "error.tooManyRequests")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// frame-ancestors 'none' is the modern CSP equivalent of
		// X-Frame-Options: DENY. Browsers that conform to CSP Level 2
		// treat frame-ancestors as authoritative and may ignore the
		// XFO header, so both must agree to keep clickjacking
		// protection on every engine.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// EscapedPath, not Path: net/url percent-decodes the target,
		// so a request for /foo%0d%0a... hands Path a literal CR LF
		// and any caller could forge extra lines in the appliance log.
		// EscapedPath keeps the on-the-wire form, which cannot carry
		// raw control bytes, and shows the operator what was actually
		// requested.
		log.Printf("%s %s %s %s", r.Method, r.URL.EscapedPath(), r.RemoteAddr, time.Since(start).Round(time.Millisecond))
	})
}
