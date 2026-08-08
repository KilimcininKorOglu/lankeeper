// This file is deliberately NOT named vendored_js_test.go. Go reads the
// trailing _<name> of a source file as a GOOS or GOARCH constraint, and
// js is a real GOOS (WebAssembly), so that name compiles only under
// GOOS=js and is silently ignored everywhere else. The tests inside it
// would never run and nothing would say so, which is exactly the class
// of failure this file exists to catch.
package buildsys_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// htmx is the one third-party runtime dependency in the tree. It is
// vendored rather than loaded from a CDN because the router is expected
// to work on a LAN with no route to the internet, and because a CDN
// fetch would also need `script-src` to name it.
//
// The version and digest below were taken from the npm registry's own
// metadata for the release, and the tarball was checked against the
// `dist.integrity` field before the file was extracted from it.
const (
	htmxPath    = "../web/static/js/htmx.min.js"
	htmxLicence = "../web/static/js/htmx.LICENSE.txt"
	htmxVersion = "2.0.10"
	htmxSHA256  = "71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de"
)

// TestVendoredHTMXMatchesItsPinnedDigest is the regression test for the
// state this file was found in: a 212-byte placeholder that logged a
// line to the console and shipped as if it were the library. Every
// hx-post, hx-get, hx-delete and hx-confirm in the tree did nothing, and
// nothing anywhere reported it. A digest is the only check that fails on
// both a placeholder and a silently swapped bundle.
func TestVendoredHTMXMatchesItsPinnedDigest(t *testing.T) {
	raw, err := os.ReadFile(htmxPath)
	if err != nil {
		t.Fatalf("read %s: %v", htmxPath, err)
	}

	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != htmxSHA256 {
		t.Errorf("%s digest = %s, want %s (htmx %s)\n"+
			"\tif this was a deliberate upgrade, verify the new tarball against the npm dist.integrity field and update both constants",
			htmxPath, got, htmxSHA256, htmxVersion)
	}
}

// TestVendoredHTMXIsNotAPlaceholder states the failure in the terms it
// actually took, so a digest mismatch caused by the placeholder coming
// back reads as what it is rather than as an upgrade gone wrong.
func TestVendoredHTMXIsNotAPlaceholder(t *testing.T) {
	raw, err := os.ReadFile(htmxPath)
	if err != nil {
		t.Fatalf("read %s: %v", htmxPath, err)
	}
	body := string(raw)

	if strings.Contains(body, "placeholder") {
		t.Fatalf("%s is a placeholder, not htmx; every hx-* attribute in the tree is inert", htmxPath)
	}
	if len(raw) < 20000 {
		t.Errorf("%s is %d bytes, far below a real htmx bundle", htmxPath, len(raw))
	}
	// The library defines this global; a bundle that does not is not the
	// one the templates are written against.
	if !strings.Contains(body, "htmx") {
		t.Errorf("%s does not look like htmx", htmxPath)
	}
}

// TestVendoredHTMXKeepsItsLicence holds the redistribution terms next to
// the file they cover. htmx is 0BSD, which asks for nothing, but a
// vendored bundle with no licence beside it gives the next reader no way
// to tell what they are allowed to do with it.
func TestVendoredHTMXKeepsItsLicence(t *testing.T) {
	raw, err := os.ReadFile(htmxLicence)
	if err != nil {
		t.Fatalf("read %s: %v", htmxLicence, err)
	}
	if !strings.Contains(string(raw), "Zero-Clause BSD") {
		t.Errorf("%s does not carry the 0BSD text htmx is published under", htmxLicence)
	}
}

// TestLayoutsPinTheHTMXConfig keeps the two settings that make the strict
// CSP workable. allowEval false stops htmx reaching for new Function on
// the one path that uses it, which the policy blocks anyway and which
// htmx would otherwise report only as an event nothing listens for.
// selfRequestsOnly is htmx's own 2.x default, pinned so an upgrade
// cannot quietly widen where a request may go.
func TestLayoutsPinTheHTMXConfig(t *testing.T) {
	for _, path := range []string{
		"../web/templates/layouts/base.html",
		"../web/templates/layouts/auth.html",
	} {
		body := readAsset(t, path)

		if !strings.Contains(body, `name="htmx-config"`) {
			t.Errorf("%s does not pin htmx-config", path)
			continue
		}
		for _, want := range []string{`"allowEval":false`, `"selfRequestsOnly":true`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s htmx-config is missing %s", path, want)
			}
		}
		// The config has to be parsed before the library initialises, so
		// the meta tag must come first in the document.
		if strings.Index(body, `name="htmx-config"`) > strings.Index(body, "/static/js/htmx.min.js") {
			t.Errorf("%s loads htmx before the config meta tag, so the config is ignored", path)
		}
	}
}

// platformTokens are the GOOS and GOARCH values Go reads from a source
// file's trailing _<name>. The list is the one that matters for a file
// name, not the full set of ports: a name only collides if it happens to
// end in one of these.
var platformTokens = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"js": true, "linux": true, "nacl": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
	"386": true, "amd64": true, "arm": true, "arm64": true,
	"loong64": true, "mips": true, "mipsle": true, "mips64": true,
	"mips64le": true, "ppc64": true, "ppc64le": true, "riscv64": true,
	"s390x": true, "sparc64": true, "wasm": true,
}

// TestNoTestFileIsAccidentallyPlatformConstrained catches the naming
// trap this file walked into. Go treats the trailing _<name> of a source
// file as an implicit build constraint, so buildsys/vendored_js_test.go
// compiled only under GOOS=js. Every test in it was skipped on every
// real build, `go test ./...` reported ok, and nothing named the file.
//
// A test that never runs is worse than no test: it reads as coverage.
func TestNoTestFileIsAccidentallyPlatformConstrained(t *testing.T) {
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}

		base := strings.TrimSuffix(name, "_test.go")
		parts := strings.Split(base, "_")
		if last := parts[len(parts)-1]; len(parts) > 1 && platformTokens[last] {
			t.Errorf("%s builds only under %s, so its tests never run here; rename it", path, last)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
