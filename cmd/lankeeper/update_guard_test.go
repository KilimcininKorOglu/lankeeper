package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestUpdateGuardRestoresAnUnconfirmedUpdate covers the case the guard
// exists for: the state file is still there when the timer fires, so the
// new release was never confirmed and the previous binary goes back.
func TestUpdateGuardRestoresAnUnconfirmedUpdate(t *testing.T) {
	dir := t.TempDir()
	state, bin, bak := filepath.Join(dir, "state.json"), filepath.Join(dir, "lankeeper"), filepath.Join(dir, "lankeeper.bak")
	writeFile(t, state, "{}")
	writeFile(t, bin, "new")
	writeFile(t, bak, "old")

	restarted := false
	if err := rollbackUnconfirmedUpdate(state, bin, bak, func() error { restarted = true; return nil }); err != nil {
		t.Fatalf("guard: %v", err)
	}
	got, err := os.ReadFile(bin)
	if err != nil || string(got) != "old" {
		t.Errorf("binary = %q, %v; want the previous binary", got, err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Error("the state file survived the rollback")
	}
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Error("the backup binary survived the rollback")
	}
	if !restarted {
		t.Error("the target was not restarted on the previous binary")
	}
}

// TestUpdateGuardLeavesAConfirmedUpdate is the other half: ConfirmUpdate
// removed the state file, so the guard must not touch the binary.
func TestUpdateGuardLeavesAConfirmedUpdate(t *testing.T) {
	dir := t.TempDir()
	bin, bak := filepath.Join(dir, "lankeeper"), filepath.Join(dir, "lankeeper.bak")
	writeFile(t, bin, "new")

	if err := rollbackUnconfirmedUpdate(filepath.Join(dir, "state.json"), bin, bak, func() error {
		t.Error("a confirmed update was restarted")
		return nil
	}); err != nil {
		t.Fatalf("guard: %v", err)
	}
	if got, _ := os.ReadFile(bin); string(got) != "new" {
		t.Errorf("binary = %q; a confirmed update was rolled back", got)
	}
}
