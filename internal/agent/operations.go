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
	"dhclient": true, "df": true,
	"cp": true, "chmod": true, "rm": true, "kill": true,
	"chpasswd": true, "localectl": true, "loadkeys": true,
	"easyrsa": true, "mkdir": true, "tail": true, "update-grub": true,
	"dhcp6c": true, "dhcp6ctl": true, "mkcert": true, "systemd-run": true,
}

// argValidators constrain the argv of a command whose name alone would
// hand the caller root. systemd-run starts any command line as a root
// unit, so it is accepted only in the one shape the OTA guard uses; the
// file, account and service commands are checked in argrules.go.
var argValidators = map[string]func([]string) error{
	"systemd-run": validateUpdateGuardArgs,
	"cp":          validateCpArgs,
	"rm":          validateRmArgs,
	"chmod":       validateChmodArgs,
	"chpasswd":    validateChpasswdArgs,
	"systemctl":   validateSystemctlArgs,
	"mkdir":       validateMkdirArgs,
	"mount":       validateMountArgs,
	"tar":         validateTarArgs,
}

// UpdateGuardUnit names the transient unit that rolls an unconfirmed OTA
// update back. The guard runs from the backup of the previous binary.
const (
	UpdateGuardUnit   = "lankeeper-update-guard"
	UpdateGuardBinary = "/usr/local/bin/lankeeper.bak"
)

// validateUpdateGuardArgs accepts exactly
// --unit=lankeeper-update-guard --on-active=<seconds> <backup binary> update-guard.
// resolveExistingPrefix resolves symlinks in the longest prefix of path
// that exists and appends the rest. Resolving only the full path or its
// parent let a missing intermediate directory hide a symlink above it:
// with L -> /etc, "/var/lib/lankeeper/L/new/x" stayed unresolved and
// matched the /var/lib/lankeeper rule while MkdirAll created /etc/new.
func resolveExistingPrefix(path string) string {
	dir, rest := path, ""
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// validateInvocation applies the argument and stdin rules registered for
// the command, if any.
func validateInvocation(baseName string, params ExecParams) error {
	if validate, ok := argValidators[baseName]; ok {
		if err := validate(params.Args); err != nil {
			return err
		}
	}
	if validate, ok := stdinValidators[baseName]; ok {
		return validate(params.Stdin)
	}
	return nil
}

func validateUpdateGuardArgs(args []string) error {
	if len(args) != 4 ||
		args[0] != "--unit="+UpdateGuardUnit ||
		args[2] != UpdateGuardBinary ||
		args[3] != "update-guard" {
		return fmt.Errorf("systemd-run: only the OTA update guard may be started")
	}
	secs, ok := strings.CutPrefix(args[1], "--on-active=")
	if !ok || secs == "" || strings.Trim(secs, "0123456789") != "" {
		return fmt.Errorf("systemd-run: --on-active must be a number of seconds")
	}
	return nil
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
	{"/var/backups/lankeeper-pre-update-", filenamePrefix},
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
		// Resolved the same way checkPathRules resolves a request, so a
		// pattern whose directory does not exist yet still matches once
		// a symlinked ancestor such as /var -> /private/var is resolved.
		switch r.kind {
		case dirPrefix:
			resolved[i].pattern = resolveExistingPrefix(strings.TrimSuffix(r.pattern, "/")) + "/"
		case exactFile:
			resolved[i].pattern = resolveExistingPrefix(r.pattern)
		case filenamePrefix:
			resolved[i].pattern = filepath.Join(resolveExistingPrefix(filepath.Dir(r.pattern)), filepath.Base(r.pattern))
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
	case "mkcert":
		// mkcert keeps its CA here and reads the location from the
		// environment. Left to the caller it would be a path the agent
		// executes against as root; pinned here it is the same root for
		// every invocation, which is also what makes the CA the web UI
		// hands out the one the certificates were signed with.
		return []string{"CAROOT=/var/lib/lankeeper/mkcert"}
	default:
		return nil
	}
}

// envFor keys the environment on the validated base name. Keyed on the
// raw field, a caller passing a full path such as
// /usr/share/easy-rsa/easyrsa missed its entry and easyrsa ran without
// EASYRSA_PKI, building the PKI where the OpenVPN server never reads it.
func envFor(params ExecParams) []string {
	return commandEnv(filepath.Base(params.Cmd))
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
	if err := validateInvocation(baseName, params); err != nil {
		return nil, err
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	// The variable command IS the design. cmdPath comes from
	// allowedCommands above, a 45-entry whitelist checked before
	// this line, and arguments are validated at the service
	// boundary because the whitelist matches the base name only.
	// #nosec G204
	cmd := exec.CommandContext(ctx, cmdPath, params.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if params.Stdin != "" {
		cmd.Stdin = strings.NewReader(params.Stdin)
	}
	if extra := envFor(params); len(extra) > 0 {
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

// validateFileMode rejects a requested mode the agent will not create
// anything with, whatever the path.
//
// The agent writes every one of these files as root, so the mode is the
// only thing standing between a config file and the rest of the system.
// Nothing this binary writes needs to be group- or world-writable, and
// nothing it writes is an executable that could want a set-id or sticky
// bit, so a request carrying any of them did not come from a caller
// serving a purpose the agent supports. Refusing is preferred over
// quietly narrowing: a caller that asked for the wrong thing should hear
// about it rather than have the request half-honoured.
func validateFileMode(mode os.FileMode) error {
	if mode&^os.FileMode(0o777) != 0 {
		return fmt.Errorf("mode %v carries bits outside the permission set", mode)
	}
	if mode&0o022 != 0 {
		return fmt.Errorf("mode %v is group- or world-writable", mode)
	}
	return nil
}

func opFileWrite(_ context.Context, raw json.RawMessage) (any, error) {
	var params FileWriteParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if !checkPathRules(params.Path, allowedWriteRules) {
		return nil, fmt.Errorf("write not allowed to path: %s", params.Path)
	}

	// params.Mode is a JSON field from an authenticated peer
	// (root or the service account), and a zero or oversized
	// value falls back to the 0o644 default on the next line.
	// #nosec G115
	mode := os.FileMode(params.Mode)
	if mode == 0 {
		mode = 0o644
	}
	if err := validateFileMode(mode); err != nil {
		return nil, err
	}

	if params.MkdirP {
		// 0755, not 0750: these are /etc directories whose files the
		// system daemons read as their own unprivileged users.
		// The path is confined by allowedWriteRules beforehand.
		// #nosec G301
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

	// Same as the write path: an authenticated peer's mode,
	// with a 0o755 default when it is zero.
	// #nosec G115
	mode := os.FileMode(params.Mode)
	if mode == 0 {
		mode = 0o755
	}
	if err := validateFileMode(mode); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(params.Path, mode); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	return map[string]string{"status": "ok"}, nil
}

func checkPathRules(path string, rules []pathRule) bool {
	// The syscalls receive the caller's string, so that is the string
	// that has to be checked. filepath.Clean removes ".." lexically while
	// the kernel resolves it after following symlinks, so a path that is
	// not already in clean absolute form is refused instead of normalised.
	if !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return false
	}
	clean := resolveExistingPrefix(path)
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
