package handlers

import "net/http"

// The two functions below answer a completed mutation. htmx submits in
// the background and swaps nothing on these routes, so it needs a bare
// 200 plus a header telling the page what to do. A plain form submission
// is a full navigation and needs a redirect, or the operator is left
// looking at an empty response body.
//
// Both blocks were written out in full at 67 call sites across 16
// handler files. Nothing detected drift between them, and any change to
// the contract, another header or a different redirect status, meant
// editing every one.

// respondRefresh tells htmx to reload the page, for a change whose
// effects are spread across the view.
func respondRefresh(w http.ResponseWriter, r *http.Request, redirectPath string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectPath, http.StatusSeeOther)
}

// respondTrigger fires a named client event instead, for a change the
// page updates in place.
func respondTrigger(w http.ResponseWriter, r *http.Request, event, redirectPath string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", event)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectPath, http.StatusSeeOther)
}
