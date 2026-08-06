package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/agent"
)

func newOpsServer(t *testing.T) *agent.Server {
	t.Helper()
	srv := agent.NewServer(filepath.Join(t.TempDir(), "s"))
	agent.RegisterBuiltinOps(srv)
	return srv
}

// resolvedTmp returns /tmp with any platform symlink already followed.
//
// The rule patterns are themselves normalised through EvalSymlinks at
// init, so on a system where /tmp is a symlink (macOS points it at
// /private/tmp) the stored pattern is the resolved one. A symlink test
// that passes a /tmp path would then be refused simply because the
// prefix does not match, which looks like the guard working while
// proving nothing. Starting from the resolved base removes that
// ambiguity, leaving the final component's symlink as the only thing
// under test on every platform.
func resolvedTmp(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Skipf("cannot resolve /tmp: %v", err)
	}
	return real
}

// TestFileReadEnforcesItsOwnRuleSet covers a privileged operation no
// test called. Read and write use separate rule tables, so a path being
// writable says nothing about it being readable, and the two lists do
// differ.
func TestFileReadEnforcesItsOwnRuleSet(t *testing.T) {
	srv := newOpsServer(t)

	// Seed a file under an allowed read prefix that also exists on a
	// developer machine without root.
	dir := t.TempDir()
	allowed := filepath.Join(dir, "lankeeper-read-probe.txt")
	if err := os.WriteFile(allowed, []byte("probe\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("blocked path is refused", func(t *testing.T) {
		params, _ := json.Marshal(agent.FileReadParams{Path: "/etc/shadow"})
		if _, err := dispatchMethod(srv, "file.read", params); err == nil {
			t.Error("file.read returned /etc/shadow")
		}
	})

	t.Run("path outside every rule is refused", func(t *testing.T) {
		params, _ := json.Marshal(agent.FileReadParams{Path: allowed})
		_, err := dispatchMethod(srv, "file.read", params)
		if err == nil {
			t.Fatal("a temp path matching no rule was read")
		}
		if !strings.Contains(err.Error(), "read not allowed") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// TestFileReadAllowsAWhitelistedTempFile exercises the positive side of
// the filenamePrefix rule through the real operation. /tmp/lankeeper-
// is the scratch pattern the firewall and backup paths use.
func TestFileReadAllowsAWhitelistedTempFile(t *testing.T) {
	srv := newOpsServer(t)

	path := "/tmp/lankeeper-read-rule-probe.txt"
	if err := os.WriteFile(path, []byte("probe\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	params, _ := json.Marshal(agent.FileReadParams{Path: path})
	out, err := dispatchMethod(srv, "file.read", params)
	if err != nil {
		t.Fatalf("file.read refused a whitelisted temp file: %v", err)
	}
	m, ok := out.(map[string]string)
	if !ok || m["content"] != "probe\n" {
		t.Errorf("file.read returned %#v, want the file contents", out)
	}
}

// TestFileMkdirEnforcesTheWriteRules covers the third privileged
// operation. It shares the write table, so a directory outside it must
// be refused even though creating a directory feels harmless: mkdir
// under an arbitrary root path is a foothold.
func TestFileMkdirEnforcesTheWriteRules(t *testing.T) {
	srv := newOpsServer(t)

	t.Run("blocked", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"path": "/etc/cron.d/lankeeper-evil"})
		_, err := dispatchMethod(srv, "file.mkdir", params)
		if err == nil {
			t.Fatal("file.mkdir created a directory outside the whitelist")
		}
		if !strings.Contains(err.Error(), "mkdir not allowed") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("allowed", func(t *testing.T) {
		path := "/tmp/lankeeper-mkdir-probe"
		t.Cleanup(func() { _ = os.RemoveAll(path) })

		params, _ := json.Marshal(map[string]any{"path": path, "mode": 0o755})
		if _, err := dispatchMethod(srv, "file.mkdir", params); err != nil {
			t.Fatalf("file.mkdir refused a whitelisted path: %v", err)
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("directory not created: %v", err)
		}
	})
}

// TestFilenamePrefixMatchesAnySuffix pins what the third rule kind
// actually means. It is a prefix match on the whole cleaned path, not a
// directory boundary, so /tmp/lankeeper-anything is deliberately in
// scope. Anyone tightening this needs to know that is the contract the
// scratch-file callers depend on.
func TestFilenamePrefixMatchesAnySuffix(t *testing.T) {
	srv := newOpsServer(t)

	for _, name := range []string{
		"/tmp/lankeeper-a.conf",
		"/tmp/lankeeper-deeply-suffixed-name.tmp",
		"/tmp/nftables-candidate.conf",
	} {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { _ = os.Remove(name) })
			params, _ := json.Marshal(agent.FileWriteParams{Path: name, Content: "x"})
			if _, err := dispatchMethod(srv, "file.write", params); err != nil {
				t.Errorf("write refused for %s: %v", name, err)
			}
		})
	}

	// The neighbouring name without the trailing hyphen must not match.
	t.Run("prefix must include the hyphen", func(t *testing.T) {
		params, _ := json.Marshal(agent.FileWriteParams{Path: "/tmp/lankeeperevil", Content: "x"})
		if _, err := dispatchMethod(srv, "file.write", params); err == nil {
			_ = os.Remove("/tmp/lankeeperevil")
			t.Error("/tmp/lankeeperevil matched the /tmp/lankeeper- rule")
		}
	})
}

// TestDotSegmentsCannotEscapeAWhitelistedPrefix is the guard that makes
// the /tmp rules safe to have at all. /tmp is world-writable, so the
// path is attacker-influenced in a way /etc paths are not.
func TestDotSegmentsCannotEscapeAWhitelistedPrefix(t *testing.T) {
	srv := newOpsServer(t)

	params, _ := json.Marshal(agent.FileWriteParams{
		Path:    "/tmp/lankeeper-x/../../etc/cron.d/pwned",
		Content: "* * * * * root sh -c id\n",
	})
	if _, err := dispatchMethod(srv, "file.write", params); err == nil {
		t.Error("dot segments walked out of the whitelisted prefix")
	}
}

// TestSymlinkedTargetIsResolvedBeforeMatching is the case the report
// singled out. /tmp is world-writable, so a local account can plant a
// symlink at a name the agent will accept. Resolution has to happen
// before the rule match, or the agent writes through it as root.
func TestSymlinkedTargetIsResolvedBeforeMatching(t *testing.T) {
	srv := newOpsServer(t)

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	link := filepath.Join(resolvedTmp(t), "lankeeper-symlink-probe")
	_ = os.Remove(link)
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	params, _ := json.Marshal(agent.FileWriteParams{Path: link, Content: "overwritten\n"})
	if _, err := dispatchMethod(srv, "file.write", params); err == nil {
		t.Error("wrote through a symlink whose target is outside every rule")
	}

	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "original\n" {
		t.Errorf("the symlink target was modified: %q", got)
	}
}

// TestSymlinkedParentIsResolvedForAFileThatDoesNotExistYet covers the
// fallback branch. A write usually targets a file that is not there
// yet, so EvalSymlinks on the full path fails and only the parent can
// be resolved. That branch is what keeps a symlinked directory from
// redirecting a create.
func TestSymlinkedParentIsResolvedForAFileThatDoesNotExistYet(t *testing.T) {
	srv := newOpsServer(t)

	outsideDir := t.TempDir()
	linkDir := filepath.Join(resolvedTmp(t), "lankeeper-parent-probe")
	_ = os.Remove(linkDir)
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(linkDir) })

	target := filepath.Join(linkDir, "new-file.conf")
	params, _ := json.Marshal(agent.FileWriteParams{Path: target, Content: "x"})
	if _, err := dispatchMethod(srv, "file.write", params); err == nil {
		_ = os.Remove(filepath.Join(outsideDir, "new-file.conf"))
		t.Error("a symlinked parent redirected a create outside every rule")
	}
}
