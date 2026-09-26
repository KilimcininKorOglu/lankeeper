package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// loginCookie logs in through a and returns the session cookie it set.
func loginCookie(t *testing.T, a *Auth) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/login", nil)); err != nil {
		t.Fatalf("login: %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionName {
			return c
		}
	}
	t.Fatal("login set no session cookie")
	return nil
}

func requestWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	return r
}

// TestLogoutRevokesACapturedCookie is the regression test. The cookie is
// signed but stateless, so the cookie issued at login still carried
// authenticated=true after logout, and anyone holding a copy stayed in.
func TestLogoutRevokesACapturedCookie(t *testing.T) {
	a := NewAuth("test-secret", "")
	c := loginCookie(t, a)
	if !a.IsAuthenticated(requestWith(c)) {
		t.Fatal("a fresh session is not authenticated")
	}
	if err := a.Logout(httptest.NewRecorder(), requestWith(c)); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if a.IsAuthenticated(requestWith(c)) {
		t.Error("the cookie still authenticates after logout")
	}
}

// TestPasswordChangeEndsOtherSessions covers the leaked-password case: the
// operator who changes it keeps their session, everyone else is out.
func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	a := NewAuth("test-secret", "")
	operator := loginCookie(t, a)
	attacker := loginCookie(t, a)

	a.ChangePassword(requestWith(operator), "new-hash")

	if a.IsAuthenticated(requestWith(attacker)) {
		t.Error("another session survived the password change")
	}
	if !a.IsAuthenticated(requestWith(operator)) {
		t.Error("the session that changed the password was ended")
	}
}
