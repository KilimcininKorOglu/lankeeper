package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runNode executes a script with node and returns its stdout. The
// browser-side code is part of the security boundary here, so it is
// exercised rather than pattern-matched; node is present on the
// development machines this suite runs on and the test skips where it is
// not, since CI's Go image carries no node.
func runNode(t *testing.T, script string) string {
	t.Helper()

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the browser-side check")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}

	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("node failed: %v\n%s", err, out)
	}
	return string(out)
}
