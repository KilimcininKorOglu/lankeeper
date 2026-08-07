package agent_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/agent"
)

// TestFileWriteRejectsAWorldWritableMode is the regression test. The
// agent took the requested mode as given and only substituted a default
// when it was zero, so a caller reaching the socket could have a root
// process create a world-writable file at any whitelisted path. The path
// whitelist decided where, and nothing decided how.
func TestFileWriteRejectsAWorldWritableMode(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-mode.sock")
	agent.RegisterBuiltinOps(srv)

	const path = "/tmp/lankeeper-mode-world-writable.txt"
	t.Cleanup(func() { _ = os.Remove(path) })

	params, _ := json.Marshal(agent.FileWriteParams{
		Path:    path,
		Content: "x",
		Mode:    0o666,
	})

	_, err := dispatchMethod(srv, "file.write", params)
	if err == nil {
		t.Fatal("file.write accepted a world-writable mode")
	}
	if !strings.Contains(err.Error(), "writable") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("the file was created despite the rejection")
	}
}

// TestFileWriteRejectsAGroupWritableMode covers the other half of the
// mask; group write is the same standing grant to a smaller set.
func TestFileWriteRejectsAGroupWritableMode(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-mode.sock")
	agent.RegisterBuiltinOps(srv)

	const path = "/tmp/lankeeper-mode-group-writable.txt"
	t.Cleanup(func() { _ = os.Remove(path) })

	params, _ := json.Marshal(agent.FileWriteParams{Path: path, Content: "x", Mode: 0o660})
	if _, err := dispatchMethod(srv, "file.write", params); err == nil {
		t.Fatal("file.write accepted a group-writable mode")
	}
}

// TestFileWriteRejectsBitsOutsideThePermissionSet blocks the set-id and
// sticky bits, which Go carries above the low nine and translates at the
// syscall boundary. Nothing this binary writes is an executable, so a
// request for one describes no supported purpose.
func TestFileWriteRejectsBitsOutsideThePermissionSet(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-mode.sock")
	agent.RegisterBuiltinOps(srv)

	for name, mode := range map[string]os.FileMode{
		"setuid": os.ModeSetuid | 0o755,
		"setgid": os.ModeSetgid | 0o755,
		"sticky": os.ModeSticky | 0o755,
	} {
		path := "/tmp/lankeeper-mode-" + name + ".txt"
		t.Cleanup(func() { _ = os.Remove(path) })

		params, _ := json.Marshal(agent.FileWriteParams{
			Path:    path,
			Content: "x",
			Mode:    int(mode),
		})
		if _, err := dispatchMethod(srv, "file.write", params); err == nil {
			t.Errorf("file.write accepted the %s bit", name)
		}
	}
}

// TestFileWriteStillAcceptsEveryModeTheServicesUse keeps the guard from
// being a regression of its own: these are the modes the production call
// sites actually pass.
func TestFileWriteStillAcceptsEveryModeTheServicesUse(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-mode.sock")
	agent.RegisterBuiltinOps(srv)

	for _, mode := range []int{0o600, 0o640, 0o644, 0o755, 0} {
		path := "/tmp/lankeeper-mode-ok.txt"
		t.Cleanup(func() { _ = os.Remove(path) })

		params, _ := json.Marshal(agent.FileWriteParams{Path: path, Content: "x", Mode: mode})
		if _, err := dispatchMethod(srv, "file.write", params); err != nil {
			t.Errorf("file.write rejected mode %#o, which a service uses: %v", mode, err)
		}
	}
}

// TestFileMkdirRejectsAWorldWritableMode covers the directory operation,
// which had the same shape and the same absent ceiling.
func TestFileMkdirRejectsAWorldWritableMode(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-mode.sock")
	agent.RegisterBuiltinOps(srv)

	const path = "/tmp/lankeeper-mode-dir"
	t.Cleanup(func() { _ = os.RemoveAll(path) })

	params, _ := json.Marshal(struct {
		Path string `json:"path"`
		Mode int    `json:"mode"`
	}{Path: path, Mode: 0o777})

	if _, err := dispatchMethod(srv, "file.mkdir", params); err == nil {
		t.Fatal("file.mkdir accepted a world-writable mode")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("the directory was created despite the rejection")
	}
}
