package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// plainExportAgent records exec.run and file.write calls in order and fails
// the command named in failCmd.
type plainExportAgent struct {
	calls   []string
	failCmd string
}

func (a *plainExportAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	var p struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
		Path string   `json:"path"`
		Mode int      `json:"mode"`
	}
	_ = json.Unmarshal(raw, &p)
	switch method {
	case "exec.run":
		a.calls = append(a.calls, p.Cmd+" "+strings.Join(p.Args, " "))
		if a.failCmd != "" && p.Cmd == a.failCmd {
			return nil, errors.New("command failed")
		}
	case "file.write":
		a.calls = append(a.calls, fmt.Sprintf("write %s %o", p.Path, p.Mode))
	}
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func indexWithPrefix(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// The pre-update snapshot must be owner-only before tar writes a single
// secret into it, not restricted after tar has finished.
func TestPlainExportCreatesTheArchiveOwnerOnlyBeforeTar(t *testing.T) {
	agent := &plainExportAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	out := preUpdateSnapshotPath("v1.2.3")
	if err := NewBackupService(t.TempDir()).Export(context.Background(), out, ""); err != nil {
		t.Fatalf("export: %v", err)
	}

	rm := indexWithPrefix(agent.calls, "rm -f -- "+out)
	create := indexWithPrefix(agent.calls, "write "+out+" 600")
	tar := indexWithPrefix(agent.calls, "tar czf "+out)
	if rm < 0 || create < 0 || tar < 0 || rm >= create || create >= tar {
		t.Fatalf("want rm, then a 0600 create, then tar; calls: %v", agent.calls)
	}
}

func TestPlainExportRemovesThePartialArchiveWhenTarFails(t *testing.T) {
	agent := &plainExportAgent{failCmd: "tar"}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	out := preUpdateSnapshotPath("v1.2.3")
	if err := NewBackupService(t.TempDir()).Export(context.Background(), out, ""); err == nil {
		t.Fatal("export succeeded although tar failed")
	}
	tar := indexWithPrefix(agent.calls, "tar czf "+out)
	if tar < 0 || !slices.Contains(agent.calls[tar+1:], "rm -f -- "+out) {
		t.Fatalf("partial archive not removed; calls: %v", agent.calls)
	}
}
