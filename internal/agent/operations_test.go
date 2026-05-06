package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/agent"
)

func TestExecRunWhitelistAllowed(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-ops.sock")
	agent.RegisterBuiltinOps(srv)

	params, _ := json.Marshal(agent.ExecParams{
		Cmd:  "df",
		Args: []string{"-h"},
	})

	result, err := dispatchMethod(srv, "exec.run", params)
	if err != nil {
		t.Fatalf("exec.run should succeed for allowed command: %v", err)
	}

	var execResult agent.ExecResult
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &execResult); err != nil {
		t.Fatalf("unmarshal exec result: %v", err)
	}

	if execResult.Stdout == "" {
		t.Error("expected non-empty stdout from df")
	}
}

func TestExecRunWhitelistBlocked(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-ops.sock")
	agent.RegisterBuiltinOps(srv)

	params, _ := json.Marshal(agent.ExecParams{
		Cmd:  "curl",
		Args: []string{"http://evil.com"},
	})

	_, err := dispatchMethod(srv, "exec.run", params)
	if err == nil {
		t.Fatal("exec.run should reject non-whitelisted command 'curl'")
	}
}

func TestFileWriteAllowedPath(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-ops.sock")
	agent.RegisterBuiltinOps(srv)

	params, _ := json.Marshal(agent.FileWriteParams{
		Path:    "/tmp/lankeeper-test-write.txt",
		Content: "test content",
		Mode:    0o644,
	})

	_, err := dispatchMethod(srv, "file.write", params)
	if err != nil {
		t.Fatalf("file.write should succeed for allowed path: %v", err)
	}
}

func TestFileWriteBlockedPath(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-ops.sock")
	agent.RegisterBuiltinOps(srv)

	params, _ := json.Marshal(agent.FileWriteParams{
		Path:    "/root/.ssh/authorized_keys",
		Content: "malicious",
		Mode:    0o644,
	})

	_, err := dispatchMethod(srv, "file.write", params)
	if err == nil {
		t.Fatal("file.write should reject non-allowed path")
	}
}

func TestFileWritePathTraversalBlocked(t *testing.T) {
	srv := agent.NewServer("/tmp/test-agent-ops.sock")
	agent.RegisterBuiltinOps(srv)

	params, _ := json.Marshal(agent.FileWriteParams{
		Path:    "/etc/ppp/../../root/.bashrc",
		Content: "malicious",
		Mode:    0o644,
	})

	_, err := dispatchMethod(srv, "file.write", params)
	if err == nil {
		t.Fatal("file.write should reject path traversal")
	}
}

func dispatchMethod(srv *agent.Server, method string, params json.RawMessage) (any, error) {
	ctx := context.Background()
	handler := srv.GetHandler(method)
	if handler == nil {
		return nil, nil
	}
	return handler(ctx, params)
}
