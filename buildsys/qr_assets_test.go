package buildsys_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	qrEncoderPath = "../web/static/js/qrcode.js"
	qrModalPath   = "../web/static/js/qr-modal.js"
	baseLayout    = "../web/templates/layouts/base.html"
)

// TestQREncoderCarriesItsLicenceHeader keeps the provenance attached to
// the file. The encoder is written for this tree rather than copied
// from a package, which is exactly why the header matters: without it
// the next reader has no way to tell an original implementation from an
// unattributed copy, and anyone lifting the file out has no terms.
func TestQREncoderCarriesItsLicenceHeader(t *testing.T) {
	src := readAsset(t, qrEncoderPath)

	for _, want := range []string{"SPDX-License-Identifier: MIT", "ISO/IEC 18004"} {
		if !strings.Contains(src, want) {
			t.Errorf("%s does not carry %q in its header", qrEncoderPath, want)
		}
	}
}

// networkCall matches the ways a script can reach off the page.
var networkCall = regexp.MustCompile(`\b(?:XMLHttpRequest|importScripts)\b|\bimport\s*\(`)

// TestQREncoderMakesNoNetworkCall holds the property the feature was
// built on: the encoder is pure computation over a string it is handed.
// A fetch inside it would send a private key somewhere, and `script-src
// 'self'` would not stop it, because the request is not a script load.
func TestQREncoderMakesNoNetworkCall(t *testing.T) {
	src := readAsset(t, qrEncoderPath)

	if loc := networkCall.FindString(src); loc != "" {
		t.Errorf("%s reaches the network via %q; the encoder must stay pure", qrEncoderPath, loc)
	}
	if strings.Contains(src, "fetch(") {
		t.Errorf("%s calls fetch; the encoder must stay pure", qrEncoderPath)
	}
}

// htmlSink matches a string actually being handed to the HTML parser,
// not the identifier appearing in prose. Both files name these sinks in
// comments explaining why they are avoided, and matching the bare word
// would fail on the explanation rather than on the behaviour.
var htmlSink = regexp.MustCompile(`\.(?:innerHTML|outerHTML)\s*[+]?=|\.insertAdjacentHTML\s*\(|document\.write\s*\(`)

// TestQRAssetsUseNoHTMLSink is why the code is drawn onto a canvas. The
// payload is a WireGuard config carrying a private key. Routing it
// through a markup sink would put attacker-influenceable text (a peer
// name reaches the config) into the parser, and the canvas removes that
// question rather than answering it.
func TestQRAssetsUseNoHTMLSink(t *testing.T) {
	for _, path := range []string{qrEncoderPath, qrModalPath} {
		src := readAsset(t, path)
		if sink := htmlSink.FindString(src); sink != "" {
			t.Errorf("%s writes through %s; QR output must go to a canvas", path, sink)
		}
	}
}

// TestQRScriptsAreLoadedByTheLayout catches the wiring being dropped.
// Both files are dead weight unless the layout pulls them in, and a
// missing tag fails as a button that silently does nothing, which no
// Go test would otherwise see.
func TestQRScriptsAreLoadedByTheLayout(t *testing.T) {
	layout := readAsset(t, baseLayout)

	for _, src := range []string{"/static/js/qrcode.js", "/static/js/qr-modal.js"} {
		if !strings.Contains(layout, src) {
			t.Errorf("%s does not load %s", baseLayout, src)
		}
	}
	if !strings.Contains(layout, `id="qr-modal"`) {
		t.Errorf("%s has no qr-modal container, so the modal has nothing to open", baseLayout)
	}
}

// TestQRTriggersUseNoInlineHandler keeps the buttons compatible with the
// policy the server actually sends. `script-src 'self'` carries no
// 'unsafe-inline', so an onclick attribute never runs. The triggers are
// declared with data attributes and picked up by a delegated listener
// for that reason.
func TestQRTriggersUseNoInlineHandler(t *testing.T) {
	inline := regexp.MustCompile(`data-qr-url="[^"]*"[^>]*\son[a-z]+=`)

	for _, path := range []string{"../web/templates/pages/vpn.html", "../web/templates/pages/openvpn.html"} {
		src := readAsset(t, path)
		if !strings.Contains(src, "data-qr-url=") {
			t.Errorf("%s has no QR trigger", path)
		}
		if inline.MatchString(src) {
			t.Errorf("%s puts an inline handler on a QR trigger; CSP blocks it", path)
		}
	}
}

func readAsset(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
