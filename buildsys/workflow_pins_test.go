package buildsys

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// usesLine captures the reference of a workflow step's action and
// whatever trailing comment follows it.
var usesLine = regexp.MustCompile(`^\s*-?\s*uses:\s*(\S+)\s*(?:#\s*(.*))?$`)

// commitSHA is a full git object name. Actions accept an abbreviated
// one, but a short SHA can be made to collide with far less work than a
// full one, so a pin is only worth having at full length.
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// workflowFiles returns every workflow in the repository, so a second
// one added later is held to the same rule as ci.yml.
func workflowFiles(t *testing.T) []string {
	t.Helper()

	dir := filepath.Join("..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	if len(out) == 0 {
		t.Fatalf("no workflow files under %s", dir)
	}
	return out
}

// TestThirdPartyActionsArePinnedToACommit is the regression test. Every
// uses: reference was a moveable version tag, and one of them is
// third-party, so the code running in this pipeline could change with
// no commit appearing in this repository.
//
// GitHub's own actions/* stay on tags deliberately: Dependabot bumps
// them into a readable diff, and the trust placed in that publisher is
// already unavoidable because the runner itself comes from there.
func TestThirdPartyActionsArePinnedToACommit(t *testing.T) {
	for _, path := range workflowFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		for i, line := range strings.Split(string(raw), "\n") {
			m := usesLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}

			ref := m[1]
			owner, version, ok := strings.Cut(ref, "@")
			if !ok {
				t.Errorf("%s:%d: %q carries no version at all", path, i+1, ref)
				continue
			}
			if strings.HasPrefix(owner, "./") || strings.HasPrefix(owner, "docker://") {
				continue
			}
			if strings.HasPrefix(owner, "actions/") {
				continue
			}

			if !commitSHA.MatchString(version) {
				t.Errorf("%s:%d: third-party action %q is pinned to %q, want a full commit SHA",
					path, i+1, owner, version)
				continue
			}
			if strings.TrimSpace(m[2]) == "" {
				t.Errorf("%s:%d: %q is pinned to a bare SHA with no version comment, "+
					"so nobody can tell what it is without a network round trip", path, i+1, owner)
			}
		}
	}
}

// TestTheWorkflowStillReferencesTheLinter guards against the pin being
// applied by deleting the step it protects.
func TestTheWorkflowStillReferencesTheLinter(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	if !strings.Contains(string(raw), "golangci/golangci-lint-action@") {
		t.Error("the lint gate no longer runs golangci-lint-action")
	}
}
