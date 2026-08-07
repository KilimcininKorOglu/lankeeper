package buildsys

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryGoFileIsFormatted is the regression test. golangci-lint's
// default set contains no formatter, so gofmt drift passed every gate
// this project had: `make lint` was green, `go vet` says nothing about
// layout, and CI ran that same default set. Four files sat unformatted
// across several releases with nothing to report them.
func TestEveryGoFileIsFormatted(t *testing.T) {
	roots := []string{"../cmd", "../internal", "../buildsys", "../deploy"}

	args := append([]string{"-l"}, roots...)
	out, err := exec.Command("gofmt", args...).Output()
	if err != nil {
		t.Fatalf("gofmt -l: %v", err)
	}

	if listed := strings.TrimSpace(string(out)); listed != "" {
		t.Errorf("unformatted files:\n%s", listed)
	}
}

// TestTheFormatterStaysEnabled guards the gate rather than the files.
// Deleting .golangci.yml, or dropping gofmt from it, reopens the hole
// silently: the linter would go back to its default set and keep
// reporting zero.
func TestTheFormatterStaysEnabled(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, "formatters:") {
		t.Error("the config declares no formatters section")
	}
	if !strings.Contains(body, "- gofmt") {
		t.Error("gofmt is no longer enabled, so formatting drift passes make lint again")
	}
}
