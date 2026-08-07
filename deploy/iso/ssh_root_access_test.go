package iso_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bookwormSSHDConfig is the shape Debian 12 ships: every directive
// present but commented out, so the effective policy comes from sshd's
// built-in defaults. Both the old code and the new one have to cope with
// a commented line rather than an active one.
const bookwormSSHDConfig = `# Package generated configuration file
Port 22

#PermitRootLogin prohibit-password
#StrictModes yes

#PasswordAuthentication yes
#PermitEmptyPasswords no

UsePAM yes
`

// sshFuncSource extracts configure_ssh_root_access from the install
// script. The script cannot be run as a whole outside a fresh chroot, so
// the function is sourced on its own; it reads nothing but its two
// arguments.
func sshFuncSource(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile("post-install.sh")
	if err != nil {
		t.Fatalf("read post-install.sh: %v", err)
	}
	body := string(raw)

	const start = "configure_ssh_root_access() {"
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatal("configure_ssh_root_access is gone from the install script")
	}
	rest := body[i:]
	j := strings.Index(rest, "\n}\n")
	if j < 0 {
		t.Fatal("could not find the end of configure_ssh_root_access")
	}
	return rest[:j+len("\n}\n")]
}

// runSSHConfig writes a stock sshd_config and the given installer
// answer, runs the function against them, and returns the resulting
// file. An empty answer stands for the operator not being asked, which
// is what an upgrade from an older ISO looks like.
func runSSHConfig(t *testing.T, answer string) string {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(cfgPath, []byte(bookwormSSHDConfig), 0o600); err != nil {
		t.Fatalf("write sshd_config: %v", err)
	}

	answerPath := filepath.Join(dir, "ssh-root-password.txt")
	if answer != "" {
		if err := os.WriteFile(answerPath, []byte(answer), 0o600); err != nil {
			t.Fatalf("write answer: %v", err)
		}
	}

	script := "set -eu\n" + sshFuncSource(t) +
		"configure_ssh_root_access " + shellQuote(answerPath) + " " + shellQuote(cfgPath) + "\n"

	cmd := exec.Command("bash", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configure_ssh_root_access failed: %v\n%s", err, out)
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	return string(got)
}

// activeDirective returns the value of an uncommented directive, or "".
func activeDirective(config, name string) string {
	re := regexp.MustCompile(`(?m)^` + name + `[ \t]+(.*)$`)
	m := re.FindStringSubmatch(config)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// TestRootPasswordSSHIsNotTheDefault is the regression test. The install
// script forced PermitRootLogin yes and PasswordAuthentication yes
// unconditionally, appending them when absent, so every device built
// from this image shipped with password-guessable root SSH without the
// operator opting in. The bootstrap ruleset confined SSH to the LAN, but
// that is a network-layer control standing in for an
// authentication-layer default: on a WAN-facing gateway, any later
// misconfiguration of the LAN and WAN boundary exposes it rather than
// failing closed.
func TestRootPasswordSSHIsNotTheDefault(t *testing.T) {
	got := runSSHConfig(t, "false")

	if v := activeDirective(got, "PermitRootLogin"); v != "prohibit-password" {
		t.Errorf("PermitRootLogin = %q, want prohibit-password", v)
	}
	// Debian's own default is left alone. The only other account the
	// installer creates has a locked password, so there is nothing for
	// it to admit.
	if v := activeDirective(got, "PasswordAuthentication"); v != "" {
		t.Errorf("PasswordAuthentication was forced to %q; it should stay at Debian's default", v)
	}
}

// TestAnUnansweredInstallStillDefaultsToKeysOnly covers the case where
// the answer file never arrives, which is what an interrupted installer
// or an older ISO looks like. A missing answer must not read as consent.
func TestAnUnansweredInstallStillDefaultsToKeysOnly(t *testing.T) {
	for name, answer := range map[string]string{
		"no file at all": "",
		"empty file":     " ",
		"garbage":        "maybe",
		"capitalised":    "True",
	} {
		t.Run(name, func(t *testing.T) {
			got := runSSHConfig(t, answer)
			if v := activeDirective(got, "PermitRootLogin"); v != "prohibit-password" {
				t.Errorf("PermitRootLogin = %q, want prohibit-password", v)
			}
		})
	}
}

// TestTheOperatorCanOptIn keeps the choice real: an operator who asked
// for password root access during the install has to get it, or they
// will edit sshd_config by hand and the question becomes theatre.
func TestTheOperatorCanOptIn(t *testing.T) {
	got := runSSHConfig(t, "true")

	if v := activeDirective(got, "PermitRootLogin"); v != "yes" {
		t.Errorf("PermitRootLogin = %q, want yes", v)
	}
	if v := activeDirective(got, "PasswordAuthentication"); v != "yes" {
		t.Errorf("PasswordAuthentication = %q, want yes", v)
	}
}

// TestTheDirectiveIsWrittenExplicitly guards against relying on which
// line Debian happened to ship commented out: a commented directive
// leaves the policy to sshd's built-in default, which is not something
// this installer should be silently inheriting either way.
func TestTheDirectiveIsWrittenExplicitly(t *testing.T) {
	for _, answer := range []string{"true", "false"} {
		got := runSSHConfig(t, answer)
		if !regexp.MustCompile(`(?m)^PermitRootLogin `).MatchString(got) {
			t.Errorf("answer %q left PermitRootLogin commented out:\n%s", answer, got)
		}
		if strings.Count(got, "\nPermitRootLogin ") != 1 {
			t.Errorf("answer %q produced a duplicate PermitRootLogin:\n%s", answer, got)
		}
	}
}

// TestADirectiveIsAppendedWhenAbsent covers a config that carries no
// PermitRootLogin line at all, where a substitution alone would leave
// the policy unstated.
func TestADirectiveIsAppendedWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(cfgPath, []byte("Port 22\nUsePAM yes\n"), 0o600); err != nil {
		t.Fatalf("write sshd_config: %v", err)
	}

	script := "set -eu\n" + sshFuncSource(t) +
		"configure_ssh_root_access " + shellQuote(filepath.Join(dir, "absent.txt")) + " " + shellQuote(cfgPath) + "\n"
	if out, err := exec.Command("bash", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("configure_ssh_root_access failed: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if v := activeDirective(string(raw), "PermitRootLogin"); v != "prohibit-password" {
		t.Errorf("PermitRootLogin = %q, want prohibit-password", v)
	}
}

// TestTheInstallerAsksBeforeEnablingIt ties the default to the opt-in.
// Without the question the safer default is simply a removed feature,
// and the operator who needed password root access has no way back.
func TestTheInstallerAsksBeforeEnablingIt(t *testing.T) {
	raw, err := os.ReadFile("preseed.cfg")
	if err != nil {
		t.Fatalf("read preseed.cfg: %v", err)
	}
	body := string(raw)

	for _, want := range []string{
		// The question is declared, defaulting to no.
		"Template: lankeeper/ssh-root-password",
		"Default: false",
		// It is actually asked, not merely declared.
		"db_input critical lankeeper/ssh-root-password",
		// The answer is recorded...
		"> /tmp/ssh-root-password.txt",
		// ...and carried into the installed system, where
		// post-install.sh reads it.
		"cp /tmp/ssh-root-password.txt /target/tmp/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preseed.cfg is missing %q", want)
		}
	}
}

// TestThePromptTextSurvivesPrintf guards the delivery mechanism: the
// cdebconf templates are one giant single-quoted printf argument, so an
// apostrophe would close the string early and a percent sign would be
// read as a format specifier. Either mangles every question after it.
func TestThePromptTextSurvivesPrintf(t *testing.T) {
	raw, err := os.ReadFile("preseed.cfg")
	if err != nil {
		t.Fatalf("read preseed.cfg: %v", err)
	}
	body := string(raw)

	start := strings.Index(body, "d-i preseed/early_command string")
	if start < 0 {
		t.Fatal("early_command is gone")
	}
	end := strings.Index(body[start:], "> /tmp/hr.tmpl")
	if end < 0 {
		t.Fatal("could not find the end of the template blob")
	}
	blob := body[start : start+end]

	// Exactly the opening and closing quote of the printf argument.
	if got := strings.Count(blob, "'"); got != 2 {
		t.Errorf("the template blob holds %d single quotes, want 2; a stray apostrophe closes the string early", got)
	}
	if got := strings.Count(blob, "%"); got != 0 {
		t.Errorf("the template blob holds %d percent signs, which printf reads as format specifiers", got)
	}
}

// TestTheRenderedTemplatesAreWellFormed runs the printf the installer
// runs and parses what it produces. Quoting is only half the problem: a
// template missing its Type or Description loads as a question cdebconf
// will never display, and the installer would silently skip asking.
func TestTheRenderedTemplatesAreWellFormed(t *testing.T) {
	raw, err := os.ReadFile("preseed.cfg")
	if err != nil {
		t.Fatalf("read preseed.cfg: %v", err)
	}
	body := string(raw)

	start := strings.Index(body, "printf 'Template:")
	if start < 0 {
		t.Fatal("the template printf is gone")
	}
	end := strings.Index(body[start:], "> /tmp/hr.tmpl")
	if end < 0 {
		t.Fatal("could not find the end of the template printf")
	}
	// Drop the trailing redirection and run the printf on its own.
	stmt := strings.TrimSpace(body[start : start+end])

	out, err := exec.Command("bash", "-c", stmt).Output()
	if err != nil {
		t.Fatalf("the template printf does not run: %v", err)
	}

	blocks := strings.Split(strings.TrimSpace(string(out)), "\n\n")
	seen := make(map[string]map[string]bool, len(blocks))
	for _, block := range blocks {
		fields := make(map[string]bool)
		var name string
		for line := range strings.SplitSeq(block, "\n") {
			if strings.HasPrefix(line, " ") {
				continue // continuation of the extended description
			}
			key, value, found := strings.Cut(line, ": ")
			if !found {
				t.Errorf("not a template field: %q", line)
				continue
			}
			fields[key] = true
			if key == "Template" {
				name = value
			}
		}
		if name == "" {
			t.Errorf("a template block has no name:\n%s", block)
			continue
		}
		if !fields["Type"] || !fields["Description"] {
			t.Errorf("template %s is missing Type or Description; cdebconf would not display it", name)
		}
		seen[name] = fields
	}

	q, ok := seen["lankeeper/ssh-root-password"]
	if !ok {
		t.Fatal("the SSH question is not among the rendered templates")
	}
	if !q["Default"] {
		t.Error("the SSH question has no Default, so the safer answer is not preselected")
	}
}
