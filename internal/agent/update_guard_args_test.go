package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSystemdRunAcceptsOnlyTheUpdateGuard pins the argv check. systemd-run
// starts any command line as a root unit, so any other shape would turn
// the whitelist entry into arbitrary root execution.
func TestSystemdRunAcceptsOnlyTheUpdateGuard(t *testing.T) {
	good := []string{"--unit=lankeeper-update-guard", "--on-active=90", "/usr/local/bin/lankeeper.bak", "update-guard"}
	if err := validateUpdateGuardArgs(good); err != nil {
		t.Fatalf("the guard invocation was refused: %v", err)
	}

	bad := [][]string{
		{"/bin/sh", "-c", "id"},
		{"--unit=lankeeper-update-guard", "--on-active=90", "/bin/sh", "update-guard"},
		{"--unit=other", "--on-active=90", "/usr/local/bin/lankeeper.bak", "update-guard"},
		{"--unit=lankeeper-update-guard", "--on-active=90s;id", "/usr/local/bin/lankeeper.bak", "update-guard"},
		{"--unit=lankeeper-update-guard", "--on-active=", "/usr/local/bin/lankeeper.bak", "update-guard"},
		{"--unit=lankeeper-update-guard", "--on-active=90", "/usr/local/bin/lankeeper.bak", "serve"},
		append(append([]string(nil), good...), "extra"),
	}
	for _, args := range bad {
		if err := validateUpdateGuardArgs(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

// TestExecRunRefusesAnArbitrarySystemdRun drives the check through the
// RPC handler, so a refactor that drops the call cannot pass unnoticed.
func TestExecRunRefusesAnArbitrarySystemdRun(t *testing.T) {
	raw, err := json.Marshal(ExecParams{Cmd: "systemd-run", Args: []string{"/bin/sh", "-c", "id"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = opExecRun(context.Background(), raw)
	if err == nil {
		t.Fatal("an arbitrary systemd-run was accepted")
	}
	// On a machine without systemd-run the resolver refuses first; the
	// argv check must be the refusal wherever the binary exists.
	if _, resolveErr := resolveAllowedCommand("systemd-run"); resolveErr == nil &&
		!strings.Contains(err.Error(), "only the OTA update guard") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}
