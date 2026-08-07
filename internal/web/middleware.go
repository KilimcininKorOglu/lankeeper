package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
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
			token, err := getOrCreateCSRFToken(w, r)
			if err != nil {
				log.Printf("csrf: %v", err)
				httpErrorT(w, r, http.StatusInternalServerError, "error.internal")
				return
			}
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

		// Constant time, because the compared value is a secret. A plain
		// != short-circuits at the first differing byte, which in
		// principle leaks the token one byte at a time. The empty check
		// stays separate: ConstantTimeCompare returns 1 for two empty
		// strings, so a request with no token would otherwise match a
		// cookie that failed to be set.
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(cookie.Value)) != 1 {
			httpErrorT(w, r, http.StatusForbidden, "error.csrfInvalid")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// getOrCreateCSRFToken returns the caller's existing token or mints a
// new one.
//
// The read is checked even though crypto/rand.Read on this toolchain
// never returns an error and crashes the program if its source fails.
// The discarded return read as an unchecked error to anyone reviewing
// it, and it is one reader swap away from being one: the value goes
// straight into a security decision, so the failure has to be a refusal
// rather than a token built from whatever was in the buffer.
func getOrCreateCSRFToken(w http.ResponseWriter, r *http.Request) (string, error) {
	if c, err := r.Cookie("csrf_token"); err == nil {
		return c.Value, nil
	}
	return rotateCSRFToken(w)
}

// rotateCSRFToken issues a fresh token whether or not one exists.
//
// Called at every authentication boundary. getOrCreateCSRFToken reuses
// an existing cookie and the cookie carries no expiry, so without this
// one value survived an entire login and logout cycle for as long as the
// browser kept it. That is the classic token fixation shape: the cookie
// is deliberately not HttpOnly so client code can echo it, so anyone
// able to plant a value before authentication would keep it valid across
// the authenticated session afterwards. SameSite=Strict on both cookies
// and the LAN-only, single-admin model close the realistic delivery
// path, which is why this is defence in depth rather than a live hole.
func rotateCSRFToken(w http.ResponseWriter) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate csrf token: %w", err)
	}
	token := hex.EncodeToString(b)

	// HttpOnly is false on purpose: client code reads this
	// cookie to echo it into the X-CSRF-Token header, which is
	// the double-submit pattern. Secure and SameSite=Strict
	// are set.
	// #nosec G124
	http.SetCookie(w, &http.Cookie{
		// HttpOnly is false on purpose: client code reads this cookie
		// to echo it into the X-CSRF-Token header, which is the
		// double-submit pattern. Secure and SameSite=Strict are set.
		// #nosec G124
		Name:     "csrf_token",
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	return token, nil
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
	refill  time.Duration
	burst   int

	// stop ends the sweeper. Without it the ticker loop had no
	// cancellation input at all, so every limiter ever constructed
	// stranded a goroutine until the process exited. Three fixed
	// instances that die with the server hid that; a test binary, or any
	// future path that rebuilds a limiter on a config reload, does not.
	stop     chan struct{}
	stopOnce sync.Once
}

type tokenBucket struct {
	tokens    float64
	lastCheck time.Time
}

// NewRateLimiter builds a per-IP token bucket. refill is the time it
// takes to earn one request back, and burst is how many can be spent at
// once, so the sustained allowance is one request per refill and the
// instant allowance is burst.
//
// The interval is a Duration rather than a count because the two
// arguments used to be plain integers, and a signature reading
// NewRateLimiter(30, 60) invited exactly one reading: thirty requests
// per sixty seconds. The implementation consumed the first argument as a
// per-second refill instead, so that call site permitted thirty requests
// a second sustained, sixty times the rate its author intended, and the
// login limiter written as (1, 5) allowed an attempt every second rather
// than every five. A Duration cannot be misread as a request count.
//
// A Duration parameter does not by itself stop the old spelling from
// compiling, because an untyped constant converts to it silently and
// NewRateLimiter(30, 60) would mean thirty nanoseconds. So the interval
// also has a floor: below a millisecond the bucket refills faster than
// any HTTP client could drain it, which is a throttle that does nothing
// rather than a throttle set low. Anything at or under that floor, zero
// and negative values included, panics. Every call site passes a
// constant, which makes this a startup failure and not a runtime one.
func NewRateLimiter(refill time.Duration, burst int) *RateLimiter {
	if refill < time.Millisecond {
		panic(fmt.Sprintf("web: rate limiter refill interval %v is below the 1ms floor; "+
			"a bare integer is read as nanoseconds, pass a time.Duration such as 200*time.Millisecond", refill))
	}
	rl := &RateLimiter{
		clients: make(map[string]*tokenBucket),
		refill:  refill,
		burst:   burst,
		stop:    make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// Stop ends the cleanup goroutine. Calling it more than once is safe, so
// a caller does not have to track whether shutdown already ran.
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stop) })
}

// rateLimiterIdleWindow is both how often buckets are swept and how long
// an address has to go quiet before its bucket is dropped. A bucket that
// has been idle that long has refilled to burst anyway, so forgetting it
// costs nothing and keeps the map bounded by active clients.
const rateLimiterIdleWindow = 5 * time.Minute

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rateLimiterIdleWindow)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for ip, b := range rl.clients {
				if now.Sub(b.lastCheck) > rateLimiterIdleWindow {
					delete(rl.clients, ip)
				}
			}
			rl.mu.Unlock()
		}
	}
}

func (rl *RateLimiter) Allow(ip string) bool {
	allowed, _ := rl.allow(ip)
	return allowed
}

// allow decides the request and, when it refuses, reports how long the
// address must wait to earn its next token. The bucket already holds
// everything needed to answer that, so a refusal that says only "no"
// leaves a client guessing.
func (rl *RateLimiter) allow(ip string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.clients[ip]
	if !ok {
		rl.clients[ip] = &tokenBucket{
			tokens:    float64(rl.burst) - 1,
			lastCheck: now,
		}
		return true, 0
	}

	elapsed := now.Sub(b.lastCheck)
	b.tokens += float64(elapsed) / float64(rl.refill)
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastCheck = now

	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(rl.refill))
	}

	b.tokens--
	return true, 0
}

// setRetryAfter states how long the caller should wait before trying
// again.
//
// A browser shows the operator the error and stops, but /metrics carries
// no session and is meant for a scraper, and any automation against this
// API is in the same position: without the header a refusal reads as
// "try again now", which is how a throttled client turns into a tight
// retry loop against the thing that was already overloaded.
//
// RFC 9110 defines the delta-seconds form as a non-negative integer, and
// a sub-second wait rounds down to zero, which would say the opposite of
// what is meant. Round up, and never emit less than one second.
func setRetryAfter(w http.ResponseWriter, wait time.Duration) {
	secs := max(int64(math.Ceil(wait.Seconds())), 1)
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := clientIP(r)

		if allowed, wait := rl.allow(host); !allowed {
			setRetryAfter(w, wait)
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
		//
		// base-uri and form-action do not fall back to default-src, so
		// omitting them left both unrestricted no matter how strict the
		// rest of the policy was. Nothing exploits that today, since
		// template auto-escaping is intact and there is no HTML
		// injection primitive to pair it with. They are here so that if
		// one is ever introduced, an injected <base> cannot silently
		// repoint every relative URL on the page and an injected form
		// cannot post the admin's credentials off-site. No template
		// carries a <base> tag and every form target is same-origin, so
		// 'self' costs nothing.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status a handler wrote so the request log
// can carry it.
//
// Flush is forwarded because the SSE endpoint asserts http.Flusher on
// the writer it is handed; a wrapper without it would turn every event
// stream into "streaming unsupported". Unwrap is there for
// http.ResponseController, which reaches the underlying writer through
// it.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	// A handler that writes a body without calling WriteHeader gets an
	// implicit 200, and the log has to say so.
	if !s.written {
		s.status = http.StatusOK
		s.written = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// statusOrDefault reports what net/http will have sent. A handler that
// returns without writing anything still produces a 200.
func (s *statusRecorder) statusOrDefault() int {
	if !s.written {
		return http.StatusOK
	}
	return s.status
}

func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		// EscapedPath, not Path: net/url percent-decodes the target,
		// so a request for /foo%0d%0a... hands Path a literal CR LF
		// and any caller could forge extra lines in the appliance log.
		// EscapedPath keeps the on-the-wire form, which cannot carry
		// raw control bytes, and shows the operator what was actually
		// requested.
		// EscapedPath percent-encodes CR and LF; Method is restricted
		// by net/http; RemoteAddr is kernel-supplied.
		// #nosec G706
		log.Printf("%s %s %d %s %s", r.Method, r.URL.EscapedPath(), rec.statusOrDefault(), r.RemoteAddr, time.Since(start).Round(time.Millisecond))
	})
}
