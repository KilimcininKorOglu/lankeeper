package handlers

import "net/http"

// setSecretDownloadHeaders prepares a response that carries key
// material: a WireGuard client config with its private key, an OpenVPN
// profile with an embedded certificate and key, or the encrypted config
// archive.
//
// Content-Disposition alone says how to present the body, not whether to
// keep it. Without a cache directive the browser is free to write these
// to its on-disk cache, so on a shared workstation the key stays
// recoverable long after the admin session ends. no-store is the
// directive that forbids that, for the browser and for any intermediary.
func setSecretDownloadHeaders(w http.ResponseWriter, contentType, filename string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
}
