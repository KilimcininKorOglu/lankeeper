package handlers

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// check pairs the outcome of one form validation with the locale key
// reported when it fails.
type check struct {
	failed bool
	key    string
}

// firstFailed returns the key of the first failed check, or "" when all
// of them passed. The checks are evaluated before the call, so each one
// must be a pure test of an already-read value.
func firstFailed(checks ...check) string {
	for _, c := range checks {
		if c.failed {
			return c.key
		}
	}
	return ""
}

// oneOf reports whether v equals one of allowed.
func oneOf(v string, allowed ...string) bool {
	return slices.Contains(allowed, v)
}

// formPort parses a form field as a TCP or UDP port number.
func formPort(r *http.Request, field string) (int, bool) {
	port, err := strconv.Atoi(r.FormValue(field))
	if err != nil || netutil.ValidatePort(port) != nil {
		return 0, false
	}
	return port, true
}

// optionalAddress accepts an empty value, a single IP address or a CIDR.
func optionalAddress(v string) bool {
	return v == "" || netutil.ValidateCIDR(v) == nil || netutil.ValidateIP(v) == nil
}
