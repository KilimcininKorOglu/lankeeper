package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"
)

// captureLog redirects the standard logger for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// After a compromise of the web process, what it ran as root and what it
// probed for must be in the agent's own journal.
func TestAgentLogsRefusalsAndExecutedCommands(t *testing.T) {
	buf := captureLog(t)

	raw, _ := json.Marshal(ExecParams{Cmd: "bash", Args: []string{"-c", "id"}})
	if _, err := opExecRun(context.Background(), raw); err == nil {
		t.Fatal("bash was allowed")
	}
	raw, _ = json.Marshal(FileWriteParams{Path: "/etc/cron.d/x", Content: "x"})
	if _, err := opFileWrite(context.Background(), raw); err == nil {
		t.Fatal("a write to /etc/cron.d was allowed")
	}
	raw, _ = json.Marshal(ExecParams{Cmd: "df"})
	if _, err := opExecRun(context.Background(), raw); err != nil {
		t.Fatalf("df: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"refused: command not allowed: bash", "refused: write not allowed to path: /etc/cron.d/x", "agent: exec /"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}
