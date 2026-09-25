package buildsys

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheComplexityGateIsWired keeps the cyclomatic complexity limit in
// CI. golangci-lint's default set carries no complexity linter, so before
// this gate a function over the limit passed every check in the
// pipeline. The limit and the pinned version live in the Makefile, and
// CI runs the Makefile target, so the two cannot drift apart.
func TestTheComplexityGateIsWired(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	ci, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}

	if !regexp.MustCompile(`(?m)^GOCYCLO_VERSION := v\d+\.\d+\.\d+$`).Match(makefile) {
		t.Error("the Makefile does not pin GOCYCLO_VERSION to a release")
	}
	recipe := "go run github.com/fzipp/gocyclo/cmd/gocyclo@$(GOCYCLO_VERSION) -over 10 ."
	if !strings.Contains(string(makefile), recipe) {
		t.Errorf("the cyclo target no longer runs %q", recipe)
	}
	if !regexp.MustCompile(`(?m)^\s+run: make cyclo\s*$`).Match(ci) {
		t.Error("ci.yml no longer runs make cyclo")
	}
}
