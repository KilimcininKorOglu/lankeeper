package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// recordingAgent captures the argv of every privileged command so a
// test can assert on what would actually have been executed as root.
type recordingAgent struct {
	mu     sync.Mutex
	calls  [][]string
	stdout map[string]string
	stdin  map[string]string
	failOn string
}

func (a *recordingAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return []byte(`{}`), nil
	}

	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var req struct {
		Cmd   string   `json:"cmd"`
		Args  []string `json:"args"`
		Stdin string   `json:"stdin"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.calls = append(a.calls, append([]string{req.Cmd}, req.Args...))
	if a.stdin == nil {
		a.stdin = map[string]string{}
	}
	a.stdin[req.Cmd] = req.Stdin
	out := a.stdout[req.Cmd]
	fail := a.failOn != "" && a.failOn == req.Cmd
	a.mu.Unlock()

	if fail {
		return nil, errors.New("command failed")
	}
	return json.Marshal(struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exitCode"`
	}{Stdout: out})
}

func (a *recordingAgent) argvFor(command string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if c[0] == command {
			return c
		}
	}
	return nil
}

func (a *recordingAgent) stdinFor(command string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stdin[command]
}

func (a *recordingAgent) all() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]string(nil), a.calls...)
}

func (a *recordingAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// newSystemTest installs the recording agent. Mutates the process-global
// agent client, so no t.Parallel in this file.
func newSystemTest(t *testing.T, stdout map[string]string) (*SystemService, *recordingAgent) {
	t.Helper()
	agent := &recordingAgent{stdout: stdout}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
	return NewSystemService(), agent
}

// TestSetRootPasswordSendsItOnStdin checks the password reaches chpasswd
// as one root line on stdin and appears in no command line, which any
// local account can read through /proc.
func TestSetRootPasswordSendsItOnStdin(t *testing.T) {
	const secret = "correct-horse-battery-staple"
	svc, agent := newSystemTest(t, nil)

	if err := svc.SetRootPassword(context.Background(), secret); err != nil {
		t.Fatalf("SetRootPassword: %v", err)
	}
	if argv := agent.argvFor("chpasswd"); strings.Join(argv, " ") != "chpasswd" {
		t.Fatalf("chpasswd argv = %v, want no arguments", argv)
	}
	if got := agent.stdinFor("chpasswd"); got != "root:"+secret+"\n" {
		t.Errorf("chpasswd stdin = %q, want one root line", got)
	}
	for _, argv := range agent.all() {
		if strings.Contains(strings.Join(argv, " "), secret) {
			t.Errorf("the plaintext password reached a command line: %v", argv)
		}
	}
}

// A newline in the password would add a chpasswd line for another
// account.
func TestSetRootPasswordRefusesControlCharacters(t *testing.T) {
	svc, agent := newSystemTest(t, nil)
	if err := svc.SetRootPassword(context.Background(), "long-enough\nlankeeper:x"); !errors.Is(err, ErrPasswordInvalid) {
		t.Fatalf("got %v, want ErrPasswordInvalid", err)
	}
	if agent.count() != 0 {
		t.Errorf("a refused password still ran %d privileged commands", agent.count())
	}
}

// TestSetRootPasswordRefusesAShortPassword keeps the two account paths
// agreeing on what is acceptable.
func TestSetRootPasswordRefusesAShortPassword(t *testing.T) {
	svc, agent := newSystemTest(t, nil)

	if err := svc.SetRootPassword(context.Background(), "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("got %v, want ErrPasswordTooShort", err)
	}
	if agent.count() != 0 {
		t.Errorf("a rejected password still ran %d privileged commands", agent.count())
	}
}

// TestSetRootPasswordSurfacesAFailure confirms a chpasswd failure is
// reported rather than swallowed.
func TestSetRootPasswordSurfacesAFailure(t *testing.T) {
	agent := &recordingAgent{failOn: "chpasswd"}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	if err := NewSystemService().SetRootPassword(context.Background(), "a-long-enough-password"); err == nil {
		t.Fatal("a failed chpasswd was ignored")
	}
}

func TestSetHostnameAppliesIt(t *testing.T) {
	svc, agent := newSystemTest(t, nil)

	if err := svc.SetHostname(context.Background(), "hermes"); err != nil {
		t.Fatalf("SetHostname: %v", err)
	}
	argv := agent.argvFor("hostnamectl")
	want := []string{"hostnamectl", "set-hostname", "hermes"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("hostnamectl argv = %v, want %v", argv, want)
	}
}

// TestHostnameValidation covers what may reach hostnamectl and the
// rendered unbound and dnsmasq configuration. The handler previously
// checked only that the value was non-empty and at most 63 characters.
func TestHostnameValidation(t *testing.T) {
	valid := []string{"hermes", "router-1", "a", "A0", strings.Repeat("h", 63)}
	for _, name := range valid {
		if err := ValidateHostname(name); err != nil {
			t.Errorf("ValidateHostname(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("h", 64),
		"-leading",
		"trailing-",
		"has space",
		"has.dot",
		"has/slash",
		"has\nnewline",
		"--option",
		"semi;colon",
	}
	for _, name := range invalid {
		if err := ValidateHostname(name); err == nil {
			t.Errorf("ValidateHostname(%q) was accepted", name)
		}
	}
}

func TestSetHostnameRefusesAnInvalidName(t *testing.T) {
	svc, agent := newSystemTest(t, nil)

	if err := svc.SetHostname(context.Background(), "not a hostname"); !errors.Is(err, ErrInvalidHostname) {
		t.Fatalf("got %v, want ErrInvalidHostname", err)
	}
	if agent.count() != 0 {
		t.Errorf("an invalid hostname still ran %d privileged commands", agent.count())
	}
}

func TestSetTimezoneAppliesIt(t *testing.T) {
	svc, agent := newSystemTest(t, nil)

	if err := svc.SetTimezone(context.Background(), "Europe/Istanbul"); err != nil {
		t.Fatalf("SetTimezone: %v", err)
	}
	argv := agent.argvFor("timedatectl")
	want := []string{"timedatectl", "set-timezone", "Europe/Istanbul"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("timedatectl argv = %v, want %v", argv, want)
	}
}

func TestTimezoneValidation(t *testing.T) {
	valid := []string{"UTC", "Europe/Istanbul", "America/Argentina/Buenos_Aires", "Etc/GMT+3"}
	for _, tz := range valid {
		if err := ValidateTimezone(tz); err != nil {
			t.Errorf("ValidateTimezone(%q) = %v, want nil", tz, err)
		}
	}

	invalid := []string{"", "Europe/Istanbul ", "../../etc/passwd", "Europe/Ist anbul", "a/b/c/d", "tz\nname"}
	for _, tz := range invalid {
		if err := ValidateTimezone(tz); err == nil {
			t.Errorf("ValidateTimezone(%q) was accepted", tz)
		}
	}
}

func TestRebootIssuesTheCommand(t *testing.T) {
	svc, agent := newSystemTest(t, nil)

	if err := svc.Reboot(context.Background()); err != nil {
		t.Fatalf("Reboot: %v", err)
	}
	argv := agent.argvFor("systemctl")
	want := []string{"systemctl", "reboot"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("systemctl argv = %v, want %v", argv, want)
	}
}

func TestRebootReportsAFailure(t *testing.T) {
	agent := &recordingAgent{failOn: "systemctl"}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	if err := NewSystemService().Reboot(context.Background()); err == nil {
		t.Error("a failed reboot was reported as success")
	}
}

// TestADomainCannotCarryAConfigDirective is the regression test. The
// hostname beside this field on the settings form was validated and the
// domain was not, even though it lands somewhere sharper:
// dnsmasq.conf.tmpl renders `domain={{ .Domain }}` and the RA drop-in
// renders it into a dhcp-option line. Every configs/sysconf template is
// text/template, which escapes nothing, so a newline ended the
// directive and appended another one to a file the root agent writes.
// dnsmasq directives include dhcp-script, which runs a program.
func TestADomainCannotCarryAConfigDirective(t *testing.T) {
	rejected := map[string]string{
		"newline then a directive": "lan\ndhcp-script=/tmp/evil.sh",
		"carriage return":          "lan\rdhcp-script=/tmp/evil.sh",
		"leading newline":          "\ndhcp-script=/tmp/evil.sh",
		"space splits the value":   "lan dhcp-script=/tmp/evil.sh",
		"tab":                      "lan\tevil",
		"comment then directive":   "lan#\ndhcp-script=/tmp/evil.sh",
		"quote":                    `lan"evil`,
		"empty":                    "",
		"leading dot":              ".lan",
		"trailing dot":             "lan.",
		"double dot":               "a..b",
		"leading hyphen":           "-lan",
		"trailing hyphen":          "lan-",
		"underscore":               "my_lan",
		"label over 63":            strings.Repeat("a", 64),
	}

	for name, domain := range rejected {
		t.Run(name, func(t *testing.T) {
			if err := ValidateDomain(domain); err == nil {
				t.Errorf("ValidateDomain(%q) was accepted", domain)
			}
		})
	}
}

// TestTheWholeDomainIsBounded covers the length the per-label pattern
// cannot: many short valid labels still exceed the RFC 1035 limit.
func TestTheWholeDomainIsBounded(t *testing.T) {
	long := strings.TrimSuffix(strings.Repeat("ab.", 100), ".")
	if len(long) <= maxDomainLength {
		t.Fatalf("test fixture is %d chars, needs to exceed %d", len(long), maxDomainLength)
	}
	if err := ValidateDomain(long); err == nil {
		t.Errorf("a %d character domain was accepted", len(long))
	}
}

// TestARealDomainIsAccepted keeps the guard from blocking the work it
// protects. The shipped default is the first entry.
func TestARealDomainIsAccepted(t *testing.T) {
	for _, domain := range []string{
		"lan",
		"hermes.lan",
		"home.arpa",
		"a",
		"my-network.local",
		"a1.b2.c3",
		strings.Repeat("a", 63),
	} {
		if err := ValidateDomain(domain); err != nil {
			t.Errorf("ValidateDomain(%q) was refused: %v", domain, err)
		}
	}
}
