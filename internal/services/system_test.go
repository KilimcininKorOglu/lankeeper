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
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.calls = append(a.calls, append([]string{req.Cmd}, req.Args...))
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

// TestSetRootPasswordInstallsTheHash is the coverage the finding says
// was missing entirely. This path rewrites the root account's password
// through two privileged commands and had no test of any kind.
func TestSetRootPasswordInstallsTheHash(t *testing.T) {
	const hash = "$6$rounds$abcdefghijklmnop"
	svc, agent := newSystemTest(t, map[string]string{"openssl": hash + "\n"})

	if err := svc.SetRootPassword(context.Background(), "a-long-enough-password"); err != nil {
		t.Fatalf("SetRootPassword: %v", err)
	}

	argv := agent.argvFor("usermod")
	if argv == nil {
		t.Fatal("usermod was never called, so no password was installed")
	}
	want := []string{"usermod", "-p", hash, "root"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("usermod argv = %v, want %v", argv, want)
	}
}

// TestSetRootPasswordNeverLeaksThePlaintext keeps the plaintext out of
// everything except the hashing command that needs it.
func TestSetRootPasswordNeverLeaksThePlaintext(t *testing.T) {
	const secret = "correct-horse-battery-staple"
	svc, agent := newSystemTest(t, map[string]string{"openssl": "$6$hash\n"})

	if err := svc.SetRootPassword(context.Background(), secret); err != nil {
		t.Fatalf("SetRootPassword: %v", err)
	}

	for _, argv := range agent.all() {
		if argv[0] == "openssl" {
			continue
		}
		if strings.Contains(strings.Join(argv, " "), secret) {
			t.Errorf("the plaintext password reached %v", argv)
		}
	}
}

// TestSetRootPasswordRefusesAnEmptyHash is the dangerous edge: usermod
// -p with an empty value writes an empty password field, which is a
// passwordless root account.
func TestSetRootPasswordRefusesAnEmptyHash(t *testing.T) {
	svc, agent := newSystemTest(t, map[string]string{"openssl": "   \n"})

	err := svc.SetRootPassword(context.Background(), "a-long-enough-password")
	if err == nil {
		t.Fatal("an empty hash was accepted")
	}
	if !errors.Is(err, ErrPasswordNotHashed) {
		t.Errorf("got %v, want ErrPasswordNotHashed", err)
	}
	if argv := agent.argvFor("usermod"); argv != nil {
		t.Errorf("usermod ran with an empty hash: %v", argv)
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

// TestSetRootPasswordSurfacesAFailedHash confirms a failure is reported
// rather than swallowed and followed by a usermod call.
func TestSetRootPasswordSurfacesAFailedHash(t *testing.T) {
	agent := &recordingAgent{failOn: "openssl"}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	err := NewSystemService().SetRootPassword(context.Background(), "a-long-enough-password")
	if err == nil {
		t.Fatal("a failed hashing command was ignored")
	}
	if argv := agent.argvFor("usermod"); argv != nil {
		t.Errorf("usermod ran after hashing failed: %v", argv)
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
