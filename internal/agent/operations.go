package agent

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

var allowedCommands = map[string]bool{
	"nft": true, "ip": true, "tc": true, "sysctl": true,
	"wg": true, "wg-quick": true, "pppd": true, "pppoe-server": true,
	"openvpn": true, "systemctl": true, "hostnamectl": true, "timedatectl": true,
	"unbound-control": true, "chronyc": true, "smbcontrol": true,
	"mdadm": true, "mkfs.ext4": true, "mount": true, "lsblk": true, "findmnt": true,
	"smartctl": true, "hdparm": true, "tar": true,
	"dig": true, "ping": true, "pgrep": true, "pkill": true, "killall": true,
	"dhclient": true, "chpasswd": true, "df": true,
	"cp": true, "chmod": true, "mv": true, "rm": true, "kill": true,
	"openssl": true, "usermod": true, "localectl": true, "loadkeys": true,
	"easyrsa": true, "mkdir": true,
}

type pathRuleKind int

const (
	dirPrefix      pathRuleKind = iota
	exactFile
	filenamePrefix
)

type pathRule struct {
	pattern string
	kind    pathRuleKind
}

var allowedWriteRules = []pathRule{
	{"/etc/ppp/", dirPrefix},
	{"/etc/openvpn/", dirPrefix},
	{"/etc/nftables.conf", exactFile},
	{"/etc/unbound/", dirPrefix},
	{"/etc/dnsmasq.conf", exactFile},
	{"/etc/dnsmasq.d/", dirPrefix},
	{"/etc/wireguard/", dirPrefix},
	{"/etc/samba/", dirPrefix},
	{"/etc/chrony/", dirPrefix},
	{"/etc/rsyslog.d/", dirPrefix},
	{"/etc/home-router/", dirPrefix},
	{"/etc/fstab", exactFile},
	{"/etc/pppoe-server-options", exactFile},
	{"/var/log/", dirPrefix},
	{"/tmp/nftables-", filenamePrefix},
	{"/tmp/home-router-", filenamePrefix},
}

var allowedReadRules = []pathRule{
	{"/etc/ppp/", dirPrefix},
	{"/etc/openvpn/", dirPrefix},
	{"/etc/wireguard/", dirPrefix},
	{"/etc/home-router/", dirPrefix},
	{"/etc/unbound/", dirPrefix},
	{"/etc/dnsmasq.conf", exactFile},
	{"/etc/dnsmasq.d/", dirPrefix},
	{"/etc/samba/", dirPrefix},
	{"/etc/chrony/", dirPrefix},
	{"/etc/rsyslog.d/", dirPrefix},
	{"/etc/fstab", exactFile},
	{"/var/log/", dirPrefix},
	{"/var/run/", dirPrefix},
	{"/proc/mdstat", exactFile},
	{"/tmp/nftables-", filenamePrefix},
	{"/tmp/home-router-", filenamePrefix},
}

type ExecParams struct {
	Cmd   string   `json:"cmd"`
	Args  []string `json:"args"`
	Stdin string   `json:"stdin,omitempty"`
	Env   []string `json:"env,omitempty"`
}

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

type FileWriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    int    `json:"mode"`
	MkdirP  bool   `json:"mkdirp"`
}

type FileReadParams struct {
	Path string `json:"path"`
}

func RegisterBuiltinOps(s *Server) {
	s.Register("ping", opPing)
	s.Register("exec.run", opExecRun)
	s.Register("file.write", opFileWrite)
	s.Register("file.read", opFileRead)
	s.Register("file.mkdir", opFileMkdir)
}

func opPing(_ context.Context, _ json.RawMessage) (any, error) {
	return map[string]string{"status": "pong"}, nil
}

func opExecRun(ctx context.Context, raw json.RawMessage) (any, error) {
	var params ExecParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	baseName := filepath.Base(params.Cmd)
	if !allowedCommands[baseName] {
		return nil, fmt.Errorf("command not allowed: %s", baseName)
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, params.Cmd, params.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if params.Stdin != "" {
		cmd.Stdin = strings.NewReader(params.Stdin)
	}
	if len(params.Env) > 0 {
		cmd.Env = append(os.Environ(), params.Env...)
	}

	err := cmd.Run()
	result := ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil {
		return result, fmt.Errorf("exec %s: %w (stderr: %s)", baseName, err, stderr.String())
	}

	return result, nil
}

func opFileWrite(_ context.Context, raw json.RawMessage) (any, error) {
	var params FileWriteParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if !checkPathRules(params.Path, allowedWriteRules) {
		return nil, fmt.Errorf("write not allowed to path: %s", params.Path)
	}

	mode := os.FileMode(params.Mode)
	if mode == 0 {
		mode = 0o644
	}

	if params.MkdirP {
		os.MkdirAll(filepath.Dir(params.Path), 0o755)
	}

	if err := os.WriteFile(params.Path, []byte(params.Content), mode); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	return map[string]string{"status": "ok"}, nil
}

func opFileRead(_ context.Context, raw json.RawMessage) (any, error) {
	var params FileReadParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if !checkPathRules(params.Path, allowedReadRules) {
		return nil, fmt.Errorf("read not allowed for path: %s", params.Path)
	}

	data, err := os.ReadFile(params.Path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	return map[string]string{"content": string(data)}, nil
}

func opFileMkdir(_ context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		Path string `json:"path"`
		Mode int    `json:"mode"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if !checkPathRules(params.Path, allowedWriteRules) {
		return nil, fmt.Errorf("mkdir not allowed for path: %s", params.Path)
	}

	mode := os.FileMode(params.Mode)
	if mode == 0 {
		mode = 0o755
	}

	if err := os.MkdirAll(params.Path, mode); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	return map[string]string{"status": "ok"}, nil
}

func checkPathRules(path string, rules []pathRule) bool {
	clean := filepath.Clean(path)
	for _, r := range rules {
		switch r.kind {
		case dirPrefix:
			if strings.HasPrefix(clean, r.pattern) {
				return true
			}
		case exactFile:
			if clean == r.pattern {
				return true
			}
		case filenamePrefix:
			if strings.HasPrefix(clean, r.pattern) {
				return true
			}
		}
	}
	return false
}
