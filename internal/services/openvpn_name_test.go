package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// killWatcher records anything handed to the root agent so a test can
// assert that a rejected name never reached it.
type killWatcher struct {
	mu    sync.Mutex
	calls [][]string
}

func (a *killWatcher) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
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
	a.mu.Unlock()
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

func (a *killWatcher) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// hostileNames are the shapes that mattered: the value became a pid
// file path, a PKI file path and an easyrsa argument.
var hostileNames = []string{
	"../../etc/passwd",
	"..",
	"a/../../b",
	"name with space",
	"name;id",
	"name\nsecond",
	"",
	strings.Repeat("a", 65),
}

// TestValidateOpenVPNClientNameRejectsTraversal is the regression test.
// Three handlers read the name from the URL and passed it on with no
// check at all, and the disconnect path turned it into
// /var/run/openvpn-<name>.pid, read that file, and handed the contents
// to kill through the root agent. Pointing the name at any readable
// file whose contents parse as a number therefore terminated an
// arbitrary process as root.
func TestValidateOpenVPNClientNameRejectsTraversal(t *testing.T) {
	for _, name := range hostileNames {
		if err := ValidateOpenVPNClientName(name); err == nil {
			t.Errorf("ValidateOpenVPNClientName(%q) was accepted", name)
		}
	}
}

func TestValidateOpenVPNClientNameAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"laptop", "phone-2", "site_b", "A0", strings.Repeat("a", 64)} {
		if err := ValidateOpenVPNClientName(name); err != nil {
			t.Errorf("ValidateOpenVPNClientName(%q) = %v, want nil", name, err)
		}
	}
}

// TestDisconnectRefusesATraversingNameBeforeTheKill is the important
// one: the guard has to sit ahead of the file read and the kill, not
// merely report afterwards.
func TestDisconnectRefusesATraversingNameBeforeTheKill(t *testing.T) {
	agent := &killWatcher{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewOpenVPNService(&config.Config{})
	err := svc.DisconnectClient(context.Background(), "../../etc/passwd")
	if err == nil {
		t.Fatal("a traversing name was accepted")
	}
	if !errors.Is(err, ErrInvalidClientName) {
		t.Errorf("got %v, want ErrInvalidClientName", err)
	}
	if n := agent.count(); n != 0 {
		t.Errorf("the rejected name still reached the agent %d times", n)
	}
}

// TestDisconnectDoesNotRemoveAnArbitraryFile covers the second sink:
// the pid file is removed after the kill, so a traversing name was a
// deletion primitive as well.
func TestDisconnectDoesNotRemoveAnArbitraryFile(t *testing.T) {
	netutil.SetAgentClient(&killWatcher{})
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("1\n"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	// Whatever relative dance is attempted, the guard rejects the name
	// before any path is built from it.
	svc := NewOpenVPNService(&config.Config{})
	_ = svc.DisconnectClient(context.Background(), "../../../../"+victim)

	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the file was removed: %v", err)
	}
}

// TestRevokeRefusesATraversingName keeps the PKI command argument
// covered.
func TestRevokeRefusesATraversingName(t *testing.T) {
	agent := &killWatcher{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewOpenVPNService(&config.Config{})
	if err := svc.RevokeClient(context.Background(), "../../etc/passwd"); !errors.Is(err, ErrInvalidClientName) {
		t.Fatalf("got %v, want ErrInvalidClientName", err)
	}
	if n := agent.count(); n != 0 {
		t.Errorf("easyrsa ran %d times with a rejected name", n)
	}
}

// TestConnectRefusesATraversingName covers the third handler. Its
// service was incidentally safe because it matched the name against the
// configured list first, but that is a property of the lookup, not a
// check, and it broke the moment a matching entry existed.
func TestConnectRefusesATraversingName(t *testing.T) {
	agent := &killWatcher{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := &config.Config{}
	cfg.OpenVPN.Clients = []config.OVPNClientConfig{{Name: "../../etc/passwd"}}
	svc := NewOpenVPNService(cfg)

	if err := svc.ConnectClient(context.Background(), "../../etc/passwd"); !errors.Is(err, ErrInvalidClientName) {
		t.Fatalf("got %v, want ErrInvalidClientName", err)
	}
	if n := agent.count(); n != 0 {
		t.Errorf("openvpn was launched %d times with a rejected name", n)
	}
}

// TestGenerateClientOVPNRefusesATraversingName covers the download
// path, where the name indexes two PKI files and the result is returned
// to the browser.
func TestGenerateClientOVPNRefusesATraversingName(t *testing.T) {
	svc := NewOpenVPNService(&config.Config{})
	if _, err := svc.GenerateClientOVPN("../../../etc/passwd"); !errors.Is(err, ErrInvalidClientName) {
		t.Fatalf("got %v, want ErrInvalidClientName", err)
	}
}

// TestAddClientRefusesATraversingName keeps the creation path aligned
// with the rest, so a name that could never be revoked or downloaded
// cannot be created either.
func TestAddClientRefusesATraversingName(t *testing.T) {
	agent := &killWatcher{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	svc := NewOpenVPNService(&config.Config{})
	if err := svc.AddClient(context.Background(), "../evil", false, nil, ""); !errors.Is(err, ErrInvalidClientName) {
		t.Fatalf("got %v, want ErrInvalidClientName", err)
	}
	if n := agent.count(); n != 0 {
		t.Errorf("easyrsa ran %d times with a rejected name", n)
	}
}
