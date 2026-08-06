package web

import (
	"net/http"
	"sync"

	"github.com/gorilla/sessions"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionName    = "lankeeper"
	sessionKeyAuth = "authenticated"
)

type Auth struct {
	store sessions.Store
	// mu guards passwordHash, which a password change rewrites while
	// login requests are reading it.
	mu           sync.RWMutex
	passwordHash string
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
	sess.Values[sessionKeyAuth] = true
	return sess.Save(r, w)
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.store.Get(r, sessionName)
	if err != nil {
		return err
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
	return ok && auth
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}
