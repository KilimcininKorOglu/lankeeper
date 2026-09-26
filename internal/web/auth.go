package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"

	"github.com/gorilla/sessions"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionName    = "lankeeper"
	sessionKeyAuth = "authenticated"
	sessionKeyID   = "sid"
)

type Auth struct {
	store sessions.Store
	// mu guards passwordHash, which a password change rewrites while
	// login requests are reading it, and the live session set.
	mu           sync.RWMutex
	passwordHash string
	// sessions holds the IDs of the sessions that are still valid. The
	// cookie is signed but stateless, so without a server-side record a
	// cookie issued at login stayed valid after logout and after a
	// password change. The set lives in memory, so a restart ends every
	// session.
	sessions map[string]struct{}
}

func NewAuth(secret, passwordHash string) *Auth {
	store := sessions.NewCookieStore([]byte(secret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
	return &Auth{
		store:        store,
		passwordHash: passwordHash,
		sessions:     make(map[string]struct{}),
	}
}

// SetPasswordHash swaps in a newly generated hash. Auth caches the hash
// by value rather than reading the live config, so without this the
// credential accepted at login stayed whatever it was at startup and a
// password change only took effect on the next restart.
func (a *Auth) SetPasswordHash(hash string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.passwordHash = hash
}

// ChangePassword swaps in a new hash and ends every session except the
// one that made the change, so a password changed because it leaked also
// locks out whoever already logged in with it.
func (a *Auth) ChangePassword(r *http.Request, hash string) {
	keep := a.sessionID(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.passwordHash = hash
	for id := range a.sessions {
		if id != keep {
			delete(a.sessions, id)
		}
	}
}

// sessionID returns the session ID the request's cookie carries, or "".
func (a *Auth) sessionID(r *http.Request) string {
	sess, err := a.store.Get(r, sessionName)
	if err != nil {
		return ""
	}
	id, _ := sess.Values[sessionKeyID].(string)
	return id
}

func (a *Auth) VerifyPassword(password string) bool {
	a.mu.RLock()
	hash := a.passwordHash
	a.mu.RUnlock()

	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func (a *Auth) Login(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.store.Get(r, sessionName)
	if err != nil {
		sess, _ = a.store.New(r, sessionName)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(raw[:])
	sess.Values[sessionKeyAuth] = true
	sess.Values[sessionKeyID] = id
	a.mu.Lock()
	a.sessions[id] = struct{}{}
	a.mu.Unlock()
	return sess.Save(r, w)
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.store.Get(r, sessionName)
	if err != nil {
		return err
	}
	if id, ok := sess.Values[sessionKeyID].(string); ok {
		a.mu.Lock()
		delete(a.sessions, id)
		a.mu.Unlock()
	}
	sess.Values[sessionKeyAuth] = false
	sess.Options.MaxAge = -1
	return sess.Save(r, w)
}

func (a *Auth) IsAuthenticated(r *http.Request) bool {
	sess, err := a.store.Get(r, sessionName)
	if err != nil {
		return false
	}
	auth, ok := sess.Values[sessionKeyAuth].(bool)
	if !ok || !auth {
		return false
	}
	id, _ := sess.Values[sessionKeyID].(string)
	a.mu.RLock()
	_, live := a.sessions[id]
	a.mu.RUnlock()
	return live
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}
