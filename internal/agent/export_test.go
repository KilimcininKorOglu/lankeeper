package agent

import (
	"path/filepath"
	"slices"
	"testing"
)

// AllowScratchDir admits a fresh temporary directory to both path rule
// sets for the duration of the test and returns its resolved path. The
// shipped rules name no world-writable location, so a test that needs a
// writable whitelisted path gets one of its own.
func AllowScratchDir(t testing.TB) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	allowTestRule(t, pathRule{dir + "/", dirPrefix})
	return dir
}

// AllowScratchPrefix admits every path starting with prefix, the way a
// filenamePrefix rule does, for the duration of the test.
func AllowScratchPrefix(t testing.TB, prefix string) {
	t.Helper()
	allowTestRule(t, pathRule{prefix, filenamePrefix})
}

func allowTestRule(t testing.TB, rule pathRule) {
	write, read := allowedWriteRules, allowedReadRules
	allowedWriteRules = append(slices.Clone(write), rule)
	allowedReadRules = append(slices.Clone(read), rule)
	t.Cleanup(func() { allowedWriteRules, allowedReadRules = write, read })
}
