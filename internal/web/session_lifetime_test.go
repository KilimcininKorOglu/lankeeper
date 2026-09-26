package web

import (
	"reflect"
	"testing"

	"github.com/gorilla/sessions"
)

// The server-side limit lives in each securecookie codec and has no
// getter, so it is read by reflection.
func TestSessionCodecsExpireWithTheCookie(t *testing.T) {
	a := NewAuth("0123456789abcdef0123456789abcdef", "")
	store := a.store.(*sessions.CookieStore)
	for i, c := range store.Codecs {
		// Read through reflect so the test does not import securecookie,
		// which stays a transitive dependency.
		field := reflect.ValueOf(c).Elem().FieldByName("maxAge")
		if !field.IsValid() {
			t.Fatalf("codec %d (%T) has no maxAge field", i, c)
		}
		got := field.Int()
		if got != int64(store.Options.MaxAge) {
			t.Errorf("codec %d accepts cookies for %d s, the cookie lives %d s", i, got, store.Options.MaxAge)
		}
	}
}
