package handlers

import (
	"errors"
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// fail answers a request that could not be completed.
//
// The full error always goes to the journal. What reaches the browser
// depends on where the error came from: a failure that crossed the agent
// IPC boundary carries the root process's stderr, the exact command
// name, and internal temp-file paths, none of which is the operator's
// business and all of which then persists in browser history and any HAR
// export. Those become the status text. An error our own validation
// produced says something useful and specific, so it is shown as-is.
//
// Handlers previously passed err.Error() straight to http.Error at 68
// sites, which forwarded the agent's text verbatim.
func fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	if r != nil {
		log.Printf("%s %s: %v", r.Method, r.URL.EscapedPath(), err)
	} else {
		log.Printf("request failed: %v", err)
	}

	http.Error(w, safeErrorMessage(err, status), status)
}

// clientError answers a request the operator can fix, with the message
// resolved in their language.
//
// Every visible string in this product is required to go through the
// locale files, and page rendering does. Error responses did not: they
// were English literals written straight into http.Error, so a Turkish
// operator, the primary locale, read raw English for essentially every
// validation and mutation failure across the whole administrative
// surface.
func clientError(w http.ResponseWriter, r *http.Request, status int, key string) {
	lang := ""
	if r != nil {
		lang = i18n.LangFromContext(r.Context())
	}
	http.Error(w, i18n.T(lang, key), status)
}

// clientErrorf is clientError for the few messages that must name the
// offending value. The detail comes from the request, never from an
// agent or a third party.
func clientErrorf(w http.ResponseWriter, r *http.Request, status int, key, detail string) {
	lang := ""
	if r != nil {
		lang = i18n.LangFromContext(r.Context())
	}
	http.Error(w, i18n.T(lang, key)+": "+detail, status)
}

// safeErrorMessage picks the text to send for err.
func safeErrorMessage(err error, status int) string {
	var agentErr *netutil.AgentError
	if err == nil || errors.As(err, &agentErr) {
		return http.StatusText(status)
	}
	return err.Error()
}
