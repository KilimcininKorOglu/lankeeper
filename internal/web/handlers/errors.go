package handlers

import (
	"errors"
	"log"
	"net/http"

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

// safeErrorMessage picks the text to send for err.
func safeErrorMessage(err error, status int) string {
	var agentErr *netutil.AgentError
	if err == nil || errors.As(err, &agentErr) {
		return http.StatusText(status)
	}
	return err.Error()
}
