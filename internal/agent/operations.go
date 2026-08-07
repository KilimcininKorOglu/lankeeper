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
	"sync"
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
	"easyrsa": true, "mkdir": true, "tail": true, "update-grub": true,
	"dhcp6c": true, "dhcp6ctl": true,
}

// trustedBinDirs are the only directories a whitelisted command is
// resolved from.
//
// The caller's own path string is discarded entirely. Validating a
// basename and then executing the caller's path meant the string that
// was checked and the string that ran were different values, so naming
// any file after an allowed command was enough to have the root agent
// execute it. Resolution against these directories closes that, because
// only root can write to them, and a caller who can write there already
// has what this boundary exists to withhold.
//
// Debian 12 is usr-merged, so /sbin and /bin are symlinks to their /usr
// counterparts. Both spellings are listed so resolution does not depend
// on that merge holding.
var trustedBinDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// commandOverrides pins the allowed commands that do not live in a bin
// directory. easyrsa ships as a script under /usr/share.
var commandOverrides = map[string]string{
	"easyrsa": "/usr/share/easy-rsa/easyrsa",
}

var (
	resolvedMu   sync.Mutex
	resolvedCmds = map[string]string{}
)

// resolveAllowedCommand maps a command name to the absolute path that
// will actually be executed, so the validated value and the executed
// value are the same string.
func resolveAllowedCommand(name string) (string, error) {
	if !allowedCommands[name] {
		return "", fmt.Errorf("command not allowed: %s", name)
	}

	resolvedMu.Lock()
	defer resolvedMu.Unlock()

	if p, ok := resolvedCmds[name]; ok {
		return p, nil
	}

	var candidates []string
	if p, ok := commandOverrides[name]; ok {
		candidates = []string{p}
	} else {
		candidates = make([]string, 0, len(trustedBinDirs))
		for _, dir := range trustedBinDirs {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}

	for _, p := range candidates {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		resolvedCmds[name] = p
		return p, nil
	}

	return "", fmt.Errorf("command %q is allowed but was not found in a trusted directory", name)
}

type pathRuleKind int

const (
	dirPrefix pathRuleKind = iota
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
	{"/etc/lankeeper/", dirPrefix},
	{"/etc/default/grub.d/", dirPrefix},
	{"/etc/wide-dhcpv6/", dirPrefix},
	{"/etc/dnscrypt-proxy/", dirPrefix},
	{"/etc/fstab", exactFile},
	{"/etc/pppoe-server-options", exactFile},
	{"/var/lib/lankeeper/", dirPrefix},
	{"/var/log/", dirPrefix},
	{"/tmp/nftables-", filenamePrefix},
	{"/tmp/lankeeper-", filenamePrefix},
}

var allowedReadRules = []pathRule{
	{"/etc/ppp/", dirPrefix},
	{"/etc/openvpn/", dirPrefix},
	{"/etc/wireguard/", dirPrefix},
	{"/etc/lankeeper/", dirPrefix},
	{"/etc/unbound/", dirPrefix},
	{"/etc/dnsmasq.conf", exactFile},
	{"/etc/dnsmasq.d/", dirPrefix},
	{"/etc/samba/", dirPrefix},
	{"/etc/chrony/", dirPrefix},
	{"/etc/rsyslog.d/", dirPrefix},
	{"/etc/wide-dhcpv6/", dirPrefix},
	{"/etc/dnscrypt-proxy/", dirPrefix},
	{"/etc/fstab", exactFile},
	{"/var/lib/lankeeper/", dirPrefix},
	{"/var/log/", dirPrefix},
	{"/var/run/", dirPrefix},
	{"/proc/mdstat", exactFile},
	{"/tmp/nftables-", filenamePrefix},
	{"/tmp/lankeeper-", filenamePrefix},
}

func init() {
	allowedWriteRules = resolveRulePatterns(allowedWriteRules)
	allowedReadRules = resolveRulePatterns(allowedReadRules)
}

func resolveRulePatterns(rules []pathRule) []pathRule {
	resolved := make([]pathRule, len(rules))
	for i, r := range rules {
		resolved[i] = r
		switch r.kind {
		case dirPrefix:
			dir := strings.TrimSuffix(r.pattern, "/")
			if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
				resolved[i].pattern = real + "/"
			}
		case exactFile:
			if real, err := filepath.EvalSymlinks(r.pattern); err == nil {
				resolved[i].pattern = real
			}
		case filenamePrefix:
			dir := filepath.Dir(r.pattern)
			base := filepath.Base(r.pattern)
			if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
				resolved[i].pattern = filepath.Join(real, base)
			}
		}
	}
	return resolved
}

type ExecParams struct {
	Cmd   string   `json:"cmd"`
	Args  []string `json:"args"`
	Stdin string   `json:"stdin,omitempty"`
}

// commandEnv supplies the extra environment a whitelisted command needs,
// decided here rather than accepted from the caller.
//
// The RPC used to carry an Env slice that was appended verbatim, while
// the whitelist governed only the command name. That gated which binary
// ran but not the environment it ran under, and the loader honours
// LD_PRELOAD and friends at execve time whatever the binary is. Owning
// the table on this side keeps the whitelist meaning what it says.
func commandEnv(cmd string) []string {
	switch cmd {
	case "easyrsa":
		return []string{"EASYRSA_PKI=/etc/openvpn/pki"}
	default:
		return nil
	}
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
	cmdPath, err := resolveAllowedCommand(baseName)
	if err != nil {
		return nil, err
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, cmdPath, params.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if params.Stdin != "" {
		cmd.Stdin = strings.NewReader(params.Stdin)
	}
	if extra := commandEnv(params.Cmd); len(extra) > 0 {
		cmd.Env = append(os.Environ(), extra...)
	}

	err = cmd.Run()
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
		if err := os.MkdirAll(filepath.Dir(params.Path), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir parent: %w", err)
		}
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
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	} else {
		dir := filepath.Dir(clean)
		if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
			clean = filepath.Join(resolvedDir, filepath.Base(clean))
		}
	}
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
