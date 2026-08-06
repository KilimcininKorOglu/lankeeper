package web

import (
	"net/http"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
)

// httpErrorT answers with a message resolved in the operator's language.
//
// The middleware wrote English literals directly, so a Turkish operator
// saw raw English for a rejected CSRF token, a blocked source address
// and a rate-limited request. LANOnly runs ahead of the language
// middleware, so the context may carry no language yet; the bundle then
// falls back to its own default, which is the correct behaviour for a
// request that never got far enough to state a preference.
func httpErrorT(w http.ResponseWriter, r *http.Request, status int, key string) {
	lang := ""
	if r != nil {
		lang = i18n.LangFromContext(r.Context())
	}
	http.Error(w, i18n.T(lang, key), status)
}
