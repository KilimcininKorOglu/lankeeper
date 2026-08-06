package i18n

import "sync/atomic"

// The process-wide bundle, so code that answers a request can localize
// without every constructor in the tree growing an *I18n parameter.
//
// Error responses were the gap this closes: page rendering already
// resolves every string through the template FuncMap, but handlers
// wrote English literals straight into http.Error, so a Turkish
// operator, the primary locale, saw raw English for essentially every
// validation and mutation failure. Threading the bundle through 14
// handler constructors to fix that would have been a wide change for
// something the server already owns exactly one of.
//
// Set once from NewServer, mirroring how the agent client is installed
// process-wide. An atomic pointer rather than a plain variable because
// tests replace it.
var defaultBundle atomic.Pointer[I18n]

// SetDefault installs the bundle used by the package-level T.
func SetDefault(i *I18n) {
	defaultBundle.Store(i)
}

// Default returns the installed bundle, or nil when none is set.
func Default() *I18n {
	return defaultBundle.Load()
}

// T translates through the installed bundle. With no bundle installed it
// returns the key, which is what the method form does for an unknown key
// too, so a missing bundle degrades to something visible rather than to
// an empty response.
func T(lang, key string) string {
	if b := defaultBundle.Load(); b != nil {
		return b.T(lang, key)
	}
	return key
}
