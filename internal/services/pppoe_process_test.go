package services

import (
	"os"
	"os/exec"
	"testing"
)

func TestProcessExistsSeesALiveProcess(t *testing.T) {
	if !processExists(os.Getpid()) {
		t.Fatal("processExists reports this test process as gone")
	}
}

// pid 1 belongs to root; the web process gets EPERM and must still see
// it as alive, as it must see a root pppd.
func TestProcessExistsTreatsPermissionDeniedAsAlive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root is never denied")
	}
	if !processExists(1) {
		t.Fatal("processExists reports pid 1 as gone")
	}
}

func TestProcessExistsReportsAReapedProcessAsGone(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("true not available")
	}
	if processExists(cmd.Process.Pid) {
		t.Fatal("a reaped process is reported as alive")
	}
}
