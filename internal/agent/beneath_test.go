package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// The service account owns /var/lib/lankeeper, so between the rule check
// and the write it can swap a checked directory for a symlink out of the
// tree. The write goes through the rule's root and must not follow it.
func TestWriteDoesNotFollowADirectorySwappedAfterTheCheck(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	rules := resolveRulePatterns([]pathRule{{base + "/", dirPrefix}})
	if err := os.Mkdir(filepath.Join(base, "d"), 0o755); err != nil {
		t.Fatal(err)
	}

	root, rel, err := openBeneath(filepath.Join(base, "d", "x"), rules, "write not allowed to path")
	if err != nil {
		t.Fatalf("openBeneath: %v", err)
	}
	defer func() { _ = root.Close() }()

	// The swap, after the check.
	if err := os.Remove(filepath.Join(base, "d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "d")); err != nil {
		t.Fatal(err)
	}

	if err := root.WriteFile(rel, []byte("owned"), 0o600); err == nil {
		t.Error("the write followed a symlink out of the rule's directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err == nil {
		t.Error("a file was created outside the rule's directory")
	}
}
