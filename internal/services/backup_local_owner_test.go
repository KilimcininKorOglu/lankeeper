package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// refusingAgent stands in for the root agent and fails every call, so a
// test proves that an operation stayed inside this process.
type refusingAgent struct{ calls []string }

func (a *refusingAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, method)
	return nil, errors.New("agent must not be used")
}

// A directory the agent creates is root-owned, and the archive is then
// written into it by the unprivileged web process, which fails. The
// target directory must come from this process.
func TestUploadLocalCreatesTheTargetDirectoryInProcess(t *testing.T) {
	tmp := t.TempDir()
	withLocalRoot(t, tmp)
	agent := &refusingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	src := filepath.Join(tmp, "src.tar.gz.enc")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := config.BackupTarget{Type: "local", Path: filepath.Join(tmp, "nightly")}
	if _, err := uploadLocal(src, target); err != nil {
		t.Fatalf("uploadLocal into a new subdirectory: %v (agent calls: %v)", err, agent.calls)
	}
	if len(agent.calls) != 0 {
		t.Fatalf("uploadLocal went through the agent: %v", agent.calls)
	}
}
