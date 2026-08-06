package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeCommand drops an executable script that prints marker.
func writeFakeCommand(t *testing.T, dir, name, marker string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\necho " + marker + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", p, err)
	}
	return p
}

// useTrustedDir points resolution at dir and clears the cache, both
// before and after, since the resolution table is package state.
func useTrustedDir(t *testing.T, dirs ...string) {
	t.Helper()
	origDirs := trustedBinDirs
	trustedBinDirs = dirs

	resolvedMu.Lock()
	resolvedCmds = map[string]string{}
	resolvedMu.Unlock()

	t.Cleanup(func() {
		trustedBinDirs = origDirs
		resolvedMu.Lock()
		resolvedCmds = map[string]string{}
		resolvedMu.Unlock()
	})
}

func runExec(t *testing.T, params ExecParams) (ExecResult, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	out, err := opExecRun(context.Background(), raw)
	if out == nil {
		return ExecResult{}, err
	}
	res, ok := out.(ExecResult)
	if !ok {
		t.Fatalf("opExecRun returned %T, want ExecResult", out)
	}
	return res, err
}

// TestExecRunIgnoresACallerSuppliedPath is the regression test. The
// check ran against filepath.Base while exec.CommandContext was handed
// the caller's original string, so the value that was validated and the
// value that ran were different. Naming any file after an allowed
// command was enough to have the root agent execute it.
//
// Mutates package-level resolution state, so no t.Parallel here.
func TestExecRunIgnoresACallerSuppliedPath(t *testing.T) {
	base := t.TempDir()
	trusted := filepath.Join(base, "trusted")
	hostile := filepath.Join(base, "hostile")

	writeFakeCommand(t, trusted, "nft", "TRUSTED")
	hostilePath := writeFakeCommand(t, hostile, "nft", "HOSTILE")

	useTrustedDir(t, trusted)

	res, err := runExec(t, ExecParams{Cmd: hostilePath})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(res.Stdout, "HOSTILE") {
		t.Errorf("the agent executed the caller's path as root; stdout was %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "TRUSTED") {
		t.Errorf("stdout = %q, want the trusted binary's output", res.Stdout)
	}
}

// TestExecRunAcceptsABareName keeps the ordinary call shape working;
// every service passes a bare command name.
func TestExecRunAcceptsABareName(t *testing.T) {
	trusted := t.TempDir()
	writeFakeCommand(t, trusted, "ip", "TRUSTED-IP")
	useTrustedDir(t, trusted)

	res, err := runExec(t, ExecParams{Cmd: "ip", Args: []string{"link"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(res.Stdout, "TRUSTED-IP") {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

// TestExecRunRejectsACommandOutsideTheWhitelist confirms the name check
// itself still applies, path or not.
func TestExecRunRejectsACommandOutsideTheWhitelist(t *testing.T) {
	trusted := t.TempDir()
	writeFakeCommand(t, trusted, "curl", "SHOULD-NOT-RUN")
	useTrustedDir(t, trusted)

	for _, cmd := range []string{"curl", filepath.Join(trusted, "curl"), "/usr/bin/curl"} {
		if _, err := runExec(t, ExecParams{Cmd: cmd}); err == nil {
			t.Errorf("%q was permitted", cmd)
		}
	}
}

// TestResolveAllowedCommandFailsWhenTheBinaryIsAbsent keeps a missing
// binary from silently falling through to the caller's own path.
func TestResolveAllowedCommandFailsWhenTheBinaryIsAbsent(t *testing.T) {
	useTrustedDir(t, t.TempDir())

	if _, err := resolveAllowedCommand("nft"); err == nil {
		t.Error("resolution succeeded with nothing installed")
	}
}

// TestResolveAllowedCommandSkipsDirectoriesAndNonExecutables makes sure
// a same-named directory or a plain data file does not satisfy the
// lookup and mask the real binary in a later directory.
func TestResolveAllowedCommandSkipsDirectoriesAndNonExecutables(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	third := filepath.Join(base, "third")

	if err := os.MkdirAll(filepath.Join(first, "tar"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second, "tar"), []byte("not executable"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeFakeCommand(t, third, "tar", "REAL-TAR")

	useTrustedDir(t, first, second, third)

	got, err := resolveAllowedCommand("tar")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(third, "tar"); got != want {
		t.Errorf("resolved to %q, want %q", got, want)
	}
}

// TestOverriddenCommandIgnoresTheTrustedDirs pins easyrsa, which does
// not live in a bin directory, and confirms the override wins over a
// same-named file placed in a trusted directory.
func TestOverriddenCommandIgnoresTheTrustedDirs(t *testing.T) {
	base := t.TempDir()
	trusted := filepath.Join(base, "trusted")
	share := filepath.Join(base, "share")

	writeFakeCommand(t, trusted, "easyrsa", "FROM-BIN")
	overridePath := writeFakeCommand(t, share, "easyrsa", "FROM-SHARE")

	origOverrides := commandOverrides
	commandOverrides = map[string]string{"easyrsa": overridePath}
	t.Cleanup(func() { commandOverrides = origOverrides })

	useTrustedDir(t, trusted)

	got, err := resolveAllowedCommand("easyrsa")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != overridePath {
		t.Errorf("resolved to %q, want the override %q", got, overridePath)
	}
}
