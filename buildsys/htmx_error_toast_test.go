package buildsys

import (
	"os"
	"strings"
	"testing"
)

// htmx 2 does not swap a 4xx or 5xx body, so without a listener every
// server-side refusal reaches the operator as no reaction at all.
func TestAppJSShowsHtmxErrorResponses(t *testing.T) {
	src, err := os.ReadFile("../web/static/js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)
	for _, want := range []string{"'htmx:responseError'", "'htmx:sendError'", "responseText", "showToast("} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %s; htmx error responses are dropped silently", want)
		}
	}
	for _, layout := range []string{"base.html", "auth.html"} {
		b, err := os.ReadFile("../web/templates/layouts/" + layout)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `data-network-error="{{ t .Lang "error.network" }}"`) {
			t.Errorf("%s: toast container carries no translated network error text", layout)
		}
	}
}
