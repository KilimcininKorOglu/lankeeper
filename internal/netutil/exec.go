package netutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

type AgentCaller interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

var agentClient AgentCaller

func SetAgentClient(c AgentCaller) {
	agentClient = c
}

func Run(ctx context.Context, name string, args ...string) (*ExecResult, error) {
	if agentClient != nil {
		return runViaAgent(ctx, name, args...)
	}
	return RunLocal(ctx, name, args...)
}

func RunSimple(ctx context.Context, name string, args ...string) (string, error) {
	result, err := Run(ctx, name, args...)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func RunLocal(ctx context.Context, name string, args ...string) (*ExecResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := &ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil {
		return result, fmt.Errorf("exec %s: %w (stderr: %s)", name, err, stderr.String())
	}

	return result, nil
}

type execParams struct {
	Cmd   string   `json:"cmd"`
	Args  []string `json:"args"`
	Stdin string   `json:"stdin,omitempty"`
	Env   []string `json:"env,omitempty"`
}

func runViaAgent(ctx context.Context, name string, args ...string) (*ExecResult, error) {
	return runViaAgentFull(ctx, "", nil, name, args...)
}

func runViaAgentFull(ctx context.Context, stdin string, env []string, name string, args ...string) (*ExecResult, error) {
	params := execParams{Cmd: name, Args: args, Stdin: stdin, Env: env}

	raw, err := agentClient.Call(ctx, "exec.run", params)
	if err != nil {
		return nil, fmt.Errorf("agent exec.run %s: %w", name, err)
	}

	var result ExecResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode exec result: %w", err)
	}

	return &result, nil
}

func RunWithStdin(ctx context.Context, stdin string, name string, args ...string) (string, error) {
	if agentClient != nil {
		result, err := runViaAgentFull(ctx, stdin, nil, name, args...)
		if err != nil {
			return "", err
		}
		return result.Stdout, nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("exec %s: %w (stderr: %s)", name, err, stderr.String())
	}
	return stdout.String(), nil
}

func RunWithEnv(ctx context.Context, env []string, name string, args ...string) (*ExecResult, error) {
	if agentClient != nil {
		return runViaAgentFull(ctx, "", env, name, args...)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := &ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return result, fmt.Errorf("exec %s: %w (stderr: %s)", name, err, stderr.String())
	}
	return result, nil
}

type fileWriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    int    `json:"mode"`
	MkdirP  bool   `json:"mkdirp"`
}

func WriteFile(path string, content []byte, mode os.FileMode) error {
	if agentClient != nil {
		params := fileWriteParams{
			Path:    path,
			Content: string(content),
			Mode:    int(mode),
			MkdirP:  true,
		}
		_, err := agentClient.Call(context.Background(), "file.write", params)
		if err != nil {
			return fmt.Errorf("agent file.write %s: %w", path, err)
		}
		return nil
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, content, mode)
}

func MkdirAll(path string, mode os.FileMode) error {
	if agentClient != nil {
		params := struct {
			Path string `json:"path"`
			Mode int    `json:"mode"`
		}{Path: path, Mode: int(mode)}
		_, err := agentClient.Call(context.Background(), "file.mkdir", params)
		if err != nil {
			return fmt.Errorf("agent file.mkdir %s: %w", path, err)
		}
		return nil
	}
	return os.MkdirAll(path, mode)
}

func ReadFile(path string) ([]byte, error) {
	if agentClient != nil {
		raw, err := agentClient.Call(context.Background(), "file.read", struct {
			Path string `json:"path"`
		}{Path: path})
		if err != nil {
			return nil, fmt.Errorf("agent file.read %s: %w", path, err)
		}
		var result struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode file.read: %w", err)
		}
		return []byte(result.Content), nil
	}
	return os.ReadFile(path)
}
