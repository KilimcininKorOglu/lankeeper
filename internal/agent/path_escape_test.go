package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// The path the syscall opens must be the path that was checked. A ".."
// after a symlink, or a missing directory below one, used to pass the
// rule while the kernel resolved the path outside it.
func TestCheckPathRulesRefusesSymlinkEscapes(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(allowed, "L")); err != nil {
		t.Fatal(err)
	}
	rules := resolveRulePatterns([]pathRule{{allowed + "/", dirPrefix}})

	for _, p := range []string{
		allowed + "/L/../cron.d/evil",
		allowed + "/L/newdir/sub",
		allowed + "/L/file",
		allowed + "/./file",
		"relative/file",
	} {
		if checkPathRules(p, rules) {
			t.Errorf("%s passed the rule but resolves outside it", p)
		}
	}
	for _, p := range []string{allowed + "/file", allowed + "/newdir/sub/file"} {
		if !checkPathRules(p, rules) {
			t.Errorf("%s was refused", p)
		}
	}
}
