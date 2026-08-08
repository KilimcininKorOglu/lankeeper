package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"time"
)

// staticAsset is one embedded file together with the ETag computed from
// its bytes at startup.
type staticAsset struct {
	data []byte
	etag string
}

// newStaticHandler serves the embedded asset tree with a content ETag on
// every file.
//
// http.FileServer over an embed.FS sends no validator at all: an
// embed.FS reports a zero ModTime, so ServeContent omits Last-Modified,
// and the standard library never generates an ETag on its own. The
// browser was therefore left with nothing to revalidate against and
// refetched every stylesheet and script on every page load.
//
// The policy is no-cache rather than a freshness lifetime, and that is
// deliberate. These files are compiled into the binary, so they change
// exactly when an OTA update replaces it. Any max-age would let a
// browser keep running the previous build's JavaScript against the new
// build's HTML for the rest of that lifetime, and the operator has no
// way to tell that is what they are looking at. no-cache still caches:
// the body is sent only when the ETag actually changed, so a page load
// costs conditional requests answered 304 instead of the full asset
// tree. Giving these files a max-age needs a URL that changes with the
// build - a hashed filename or a ?v= token - not a header change alone.
func newStaticHandler(fsys fs.FS) http.Handler {
	assets := make(map[string]staticAsset)

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		data, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return fmt.Errorf("read embedded asset %s: %w", p, readErr)
		}
		assets[p] = staticAsset{
			data: data,
			etag: fmt.Sprintf(`"%x"`, sha256.Sum256(data)),
		}
		return nil
	})
	// The tree is embedded in this binary, so a failure here means the
	// build is malformed rather than the environment being unusual.
	// Report it and keep serving what was read: the UI degrades to
	// missing assets, which is visible, instead of refusing to boot and
	// taking DNS, DHCP and the firewall down with it.
	if err != nil {
		log.Printf("static: indexing embedded assets: %v", err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		asset, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}

		// Set before ServeContent: it reads ETag off the header map to
		// evaluate If-None-Match, and Cache-Control has to be present
		// on the 304 as well as the 200.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", asset.etag)

		// modTime stays zero on purpose. The only value available here
		// is the process start time, which would claim every asset
		// changed on every restart. Zero makes ServeContent omit
		// Last-Modified and validate on the ETag alone, which is the
		// validator that actually tracks the content.
		//
		// ServeContent handles If-None-Match list parsing, weak
		// comparison, "*", Range, HEAD, and Content-Type from the
		// extension.
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(asset.data))
	})
}
