package agent

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// Argument rules for the whitelisted commands that act on files, accounts
// or services.
//
// The command name alone does not bound what a caller can do with cp, rm,
// chmod, tar, mount, mkdir, chpasswd or systemctl: each of them turns the
// right arguments into root. The agent is the privilege boundary, and the
// caller is the unprivileged process that boundary exists to distrust, so
// the argv is checked here against the shapes the services actually send
// and against the same path rules file.write enforces.

const (
	// installedBinary is where the installers put the lankeeper binary.
	installedBinary = "/usr/local/bin/lankeeper"
	// preUpdateSnapshotPrefix names the root-owned archives taken before
	// an OTA update.
	preUpdateSnapshotPrefix = "/var/backups/lankeeper-pre-update-"
)

// backupSourceDirs are the directories a backup archive may carry.
var backupSourceDirs = []string{"/etc/lankeeper", "/etc/unbound", "/etc/dnsmasq.d", "/etc/openvpn"}

// serviceUnits lists, per systemctl verb, the units the services manage.
var serviceUnits = map[string][]string{
	"start":             {"openvpn@server"},
	"stop":              {"openvpn@server", "dnscrypt-proxy", "lankeeper-dhcp6c.service", UpdateGuardUnit + ".timer"},
	"restart":           {"lankeeper.target", "chrony", "rsyslog", "dnscrypt-proxy", "lankeeper-dhcp6c.service"},
	"reload-or-restart": {"dnsmasq", "lankeeper-dhcp6c.service"},
	"enable":            {"dnscrypt-proxy"},
}

// cleanAbs returns p cleaned, or an error when it is relative or climbs.
func cleanAbs(p string) (string, error) {
	if !filepath.IsAbs(p) || slices.Contains(strings.Split(p, "/"), "..") {
		return "", fmt.Errorf("path %q is not a plain absolute path", p)
	}
	return filepath.Clean(p), nil
}

// writablePath reports whether a command may create, change or remove p.
func writablePath(p string) bool {
	clean, err := cleanAbs(p)
	if err != nil {
		return false
	}
	switch {
	case clean == installedBinary, clean == UpdateGuardBinary:
		return true
	case strings.HasPrefix(clean, preUpdateSnapshotPrefix):
		return true
	}
	return checkPathRules(clean, allowedWriteRules)
}

// readablePath reports whether a command may read p.
func readablePath(p string) bool {
	clean, err := cleanAbs(p)
	if err != nil {
		return false
	}
	if clean == installedBinary || clean == UpdateGuardBinary {
		return true
	}
	return checkPathRules(clean, allowedReadRules)
}

// validateCpArgs accepts "cp -f SRC DST" and "cp -- SRC DST".
func validateCpArgs(args []string) error {
	if len(args) != 3 || (args[0] != "-f" && args[0] != "--") {
		return fmt.Errorf("cp: expected -f or -- and two paths")
	}
	if !readablePath(args[1]) || !writablePath(args[2]) {
		return fmt.Errorf("cp: %s -> %s is outside the permitted paths", args[1], args[2])
	}
	return nil
}

// validateRmArgs accepts "rm -f [--] PATH...".
func validateRmArgs(args []string) error {
	if len(args) < 2 || args[0] != "-f" {
		return fmt.Errorf("rm: expected -f and at least one path")
	}
	paths := args[1:]
	if paths[0] == "--" {
		paths = paths[1:]
	}
	if len(paths) == 0 {
		return fmt.Errorf("rm: no path given")
	}
	for _, p := range paths {
		if !writablePath(p) {
			return fmt.Errorf("rm: %s is outside the permitted paths", p)
		}
	}
	return nil
}

// validateChmodArgs accepts 600 and 640 on a writable path, and +x only on
// the installed binary.
func validateChmodArgs(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("chmod: expected a mode and one path")
	}
	switch args[0] {
	case "600", "640":
		if writablePath(args[1]) {
			return nil
		}
	case "+x":
		if args[1] == installedBinary {
			return nil
		}
	}
	return fmt.Errorf("chmod: %s %s is not permitted", args[0], args[1])
}

// validateChpasswdArgs accepts chpasswd with no arguments; the account
// and password come on stdin.
func validateChpasswdArgs(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("chpasswd: no arguments are permitted")
	}
	return nil
}

// stdinValidators check the stdin of commands whose input decides what
// they change.
var stdinValidators = map[string]func(string) error{
	"chpasswd": validateChpasswdStdin,
}

// validateChpasswdStdin accepts exactly one "root:<password>" line. Any
// other account, or a second line, would let the caller set a password
// the services never set.
func validateChpasswdStdin(stdin string) error {
	line, ok := strings.CutSuffix(stdin, "\n")
	pw, isRoot := strings.CutPrefix(line, "root:")
	if !ok || !isRoot || pw == "" || strings.ContainsFunc(pw, unicode.IsControl) {
		return fmt.Errorf("chpasswd: only one root:<password> line is permitted")
	}
	return nil
}

// validateSystemctlArgs accepts reboot and the verb/unit pairs in
// serviceUnits, with --now only on enable.
func validateSystemctlArgs(args []string) error {
	if len(args) == 1 && args[0] == "reboot" {
		return nil
	}
	if len(args) == 3 && args[0] == "enable" && args[1] == "--now" && args[2] == "lankeeper-dhcp6c.service" {
		return nil
	}
	if len(args) == 2 && slices.Contains(serviceUnits[args[0]], args[1]) {
		return nil
	}
	return fmt.Errorf("systemctl: %s is not permitted", strings.Join(args, " "))
}

// mediaPath reports whether p is a plain path below /mnt or /srv.
func mediaPath(p string) bool {
	clean, err := cleanAbs(p)
	return err == nil && (strings.HasPrefix(clean, "/mnt/") || strings.HasPrefix(clean, "/srv/"))
}

// validateMkdirArgs accepts "mkdir -p DIR" below /mnt or /srv.
func validateMkdirArgs(args []string) error {
	if len(args) != 2 || args[0] != "-p" || !mediaPath(args[1]) {
		return fmt.Errorf("mkdir: only -p below /mnt or /srv is permitted")
	}
	return nil
}

// validateMountArgs accepts "mount /dev/X DIR" with DIR below /mnt or /srv.
func validateMountArgs(args []string) error {
	if len(args) != 2 || !mediaPath(args[1]) {
		return fmt.Errorf("mount: only a device onto /mnt or /srv is permitted")
	}
	dev, err := cleanAbs(args[0])
	if err != nil || !strings.HasPrefix(dev, "/dev/") {
		return fmt.Errorf("mount: %s is not a device", args[0])
	}
	return nil
}

// validateTarArgs accepts the export shape "czf OUT (-C PARENT NAME)...",
// where OUT is a staging or snapshot path and each PARENT/NAME is one of
// the backup source directories.
func validateTarArgs(args []string) error {
	if len(args) < 5 || args[0] != "czf" || (len(args)-2)%3 != 0 {
		return fmt.Errorf("tar: only the backup export shape is permitted")
	}
	if !writablePath(args[1]) {
		return fmt.Errorf("tar: %s is outside the permitted paths", args[1])
	}
	for i := 2; i < len(args); i += 3 {
		src := filepath.Join(args[i+1], args[i+2])
		if args[i] != "-C" || !slices.Contains(backupSourceDirs, src) {
			return fmt.Errorf("tar: %s is not a backup source directory", src)
		}
	}
	return nil
}
