package buildsys

import (
	"os"
	"strings"
	"testing"
)

// The per-client table's empty-state text once came from a window global
// that only an inline script could set, and the CSP blocks inline
// scripts, so the row rendered blank. The text has to travel on the
// table itself.
func TestQoSEmptyStateTextComesFromTheTemplate(t *testing.T) {
	js, err := os.ReadFile("../web/static/js/qos-chart.js")
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile("../web/templates/pages/qos.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "qosI18n") {
		t.Error("qos-chart.js still reads the window.qosI18n global")
	}
	if !strings.Contains(string(js), "getAttribute('data-empty')") {
		t.Error("qos-chart.js does not read the empty-state text from data-empty")
	}
	if !strings.Contains(string(page), `data-empty="{{ t $.Lang "qos.perClient.empty" }}"`) {
		t.Error("the per-client table carries no translated data-empty attribute")
	}
}
