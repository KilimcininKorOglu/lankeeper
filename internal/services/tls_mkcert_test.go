package services_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// mkcertFakeAgent stands in for the root agent. It answers exec.run for
// mkcert by writing a real key pair to the paths it was given, which is
// what the binary does, and mirrors the file operations onto the local
// filesystem so the staging-and-copy path is exercised end to end.
type mkcertFakeAgent struct {
	mu sync.Mutex
	// issueFails makes mkcert report failure without writing anything,
	// which is the case that must leave the previous mode intact.
	issueFails bool
	// garbage makes mkcert write something that is not a certificate,
	// standing in for a truncated or error-filled output file.
	garbage bool
	sans    []string
	removed []string
}

func (f *mkcertFakeAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	raw, _ := json.Marshal(params)
	switch method {
	case "exec.run":
		var p struct {
			Cmd  string   `json:"cmd"`
			Args []string `json:"args"`
		}
		_ = json.Unmarshal(raw, &p)
		switch p.Cmd {
		case "mkcert":
			if f.issueFails {
				return nil, errors.New("mkcert: exit status 1")
			}
			certPath, keyPath := p.Args[1], p.Args[3]
			f.sans = append([]string(nil), p.Args[4:]...)
			if f.garbage {
				_ = os.WriteFile(certPath, []byte("mkcert: not a certificate\n"), 0o644)
				_ = os.WriteFile(keyPath, []byte("nor is this\n"), 0o600)
				return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
			}
			certPEM, keyPEM := issueTestPair(f.sans)
			_ = os.WriteFile(certPath, certPEM, 0o644)
			_ = os.WriteFile(keyPath, keyPEM, 0o600)
			return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
		case "rm":
			for _, a := range p.Args {
				if a == "-f" {
					continue
				}
				f.removed = append(f.removed, a)
				_ = os.Remove(a)
			}
			return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
		}
		return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
	case "file.mkdir":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &p)
		_ = os.MkdirAll(p.Path, 0o755)
		return []byte(`{}`), nil
	case "file.read":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &p)
		data, err := os.ReadFile(p.Path) // #nosec G304 -- test fixture path
		if err != nil {
			return nil, err
		}
		out, _ := json.Marshal(map[string]string{"content": string(data)})
		return out, nil
	}
	return []byte(`{}`), nil
}

// issueTestPair builds a real certificate so the install path parses
// something valid rather than a placeholder that would pass only because
// nothing looked at it.
func issueTestPair(sans []string) (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mkcert test CA"},
		Issuer:       pkix.Name{CommonName: "mkcert test CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		DNSNames:     sans,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func newMkcertTestService(t *testing.T, agent *mkcertFakeAgent) (*services.TLSService, *testTLSConfig) {
	t.Helper()
	svc, cfg, dir := newTLSTestService(t)
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
	return svc, &testTLSConfig{cfg: cfg, dir: dir}
}

// The certificate mkcert issued has to end up where the server loads
// from, readable by the account the server runs as. mkcert runs as root
// through the agent, so a straight write to the serving paths would
// leave a key the web process cannot open at startup.
func TestEnableMkcertInstallsAServableKeyPair(t *testing.T) {
	agent := &mkcertFakeAgent{}
	svc, env := newMkcertTestService(t, agent)

	info, err := svc.EnableMkcert(context.Background(), []string{"hermes.lan", "10.10.10.1"})
	if err != nil {
		t.Fatalf("enable mkcert: %v", err)
	}

	if env.cfg.System.TLS.Mode != "mkcert" {
		t.Errorf("mode = %q, want mkcert", env.cfg.System.TLS.Mode)
	}
	if info.Issuer != "mkcert test CA" {
		t.Errorf("issuer = %q, want the mkcert CA", info.Issuer)
	}

	keyPath := filepath.Join(env.dir, "tls", "server.key")
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("private key not installed: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("private key mode = %04o, want 0600: it is readable by other local accounts", perm)
	}
	if _, err := os.Stat(filepath.Join(env.dir, "tls", "server.crt")); err != nil {
		t.Errorf("certificate not installed: %v", err)
	}
}

// The staged pair carries the private key in a directory that is not
// the guarded one. Leaving it behind is the worst outcome of this
// function and the one nothing else would notice.
func TestEnableMkcertRemovesTheStagedPair(t *testing.T) {
	agent := &mkcertFakeAgent{}
	svc, env := newMkcertTestService(t, agent)

	if _, err := svc.EnableMkcert(context.Background(), []string{"hermes.lan"}); err != nil {
		t.Fatalf("enable mkcert: %v", err)
	}

	for _, name := range []string{"staged.crt", "staged.key"} {
		if _, err := os.Stat(filepath.Join(env.dir, "mkcert", name)); !os.IsNotExist(err) {
			t.Errorf("%s survived; the staged private key is still on disk", name)
		}
	}
	if len(agent.removed) != 2 {
		t.Errorf("agent was asked to remove %d files, want 2", len(agent.removed))
	}
}

// A failure has to leave the operator exactly where they were. The mode
// is what the server reads at startup, so recording "mkcert" without a
// certificate under it is a service that cannot bind, correctable only
// from the interface that just stopped answering.
func TestEnableMkcertLeavesTheModeAloneOnFailure(t *testing.T) {
	cases := []struct {
		name  string
		agent *mkcertFakeAgent
	}{
		{"mkcert reports failure", &mkcertFakeAgent{issueFails: true}},
		{"mkcert writes something unparseable", &mkcertFakeAgent{garbage: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, env := newMkcertTestService(t, tc.agent)
			env.cfg.System.TLS.Mode = "self-signed"

			if _, err := svc.EnableMkcert(context.Background(), []string{"hermes.lan"}); err == nil {
				t.Fatal("reported success despite mkcert failing")
			}
			if env.cfg.System.TLS.Mode != "self-signed" {
				t.Errorf("mode = %q, want self-signed: a failed issuance changed the mode",
					env.cfg.System.TLS.Mode)
			}
			if _, err := os.Stat(filepath.Join(env.dir, "tls", "server.crt")); !os.IsNotExist(err) {
				t.Error("a certificate was installed despite the failure")
			}
		})
	}
}

func TestEnableMkcertRefusesInputItCannotIssueFor(t *testing.T) {
	cases := []struct {
		name string
		sans []string
		want error
	}{
		{"no names at all", nil, services.ErrNoSANs},
		{"a name that is neither host nor ip", []string{"not a host"}, services.ErrInvalidSAN},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := &mkcertFakeAgent{}
			svc, _ := newMkcertTestService(t, agent)

			_, err := svc.EnableMkcert(context.Background(), tc.sans)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if agent.sans != nil {
				t.Error("mkcert was invoked for input that should have been refused first")
			}
		})
	}
}

// The CA is the whole point of the mode: without it on their devices the
// operator still sees a warning. A missing one has to be its own
// reportable condition rather than an empty download.
func TestMkcertCAIsReadableAndMissingIsDistinguished(t *testing.T) {
	agent := &mkcertFakeAgent{}
	svc, env := newMkcertTestService(t, agent)

	if _, err := svc.MkcertCA(); !errors.Is(err, services.ErrNoMkcertCA) {
		t.Fatalf("error = %v, want ErrNoMkcertCA before a CA exists", err)
	}

	caDir := filepath.Join(env.dir, "mkcert")
	if err := os.MkdirAll(caDir, 0o755); err != nil {
		t.Fatalf("seed CA dir: %v", err)
	}
	want, _ := issueTestPair([]string{"ca"})
	if err := os.WriteFile(filepath.Join(caDir, "rootCA.pem"), want, 0o644); err != nil {
		t.Fatalf("seed CA: %v", err)
	}

	got, err := svc.MkcertCA()
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	if string(got) != string(want) {
		t.Error("the CA served back is not the one on disk")
	}
}

// SwitchMode is the one entry point the mode form posts to, so an
// unrecognised value must be refused rather than written through.
func TestSwitchModeRejectsAnUnknownMode(t *testing.T) {
	agent := &mkcertFakeAgent{}
	svc, env := newMkcertTestService(t, agent)

	_, err := svc.SwitchMode(context.Background(), "letsencrypt", "hermes.lan", nil, 30)
	if !errors.Is(err, services.ErrUnknownTLSMode) {
		t.Fatalf("error = %v, want ErrUnknownTLSMode", err)
	}
	if env.cfg.System.TLS.Mode != "self-signed" {
		t.Errorf("mode = %q, want self-signed", env.cfg.System.TLS.Mode)
	}
}

// Switching back has to work from mkcert, which is exactly the case the
// regenerate button refuses. Without it the mode is a one-way door.
func TestSwitchModeReturnsToSelfSignedFromMkcert(t *testing.T) {
	agent := &mkcertFakeAgent{}
	svc, env := newMkcertTestService(t, agent)

	if _, err := svc.EnableMkcert(context.Background(), []string{"hermes.lan"}); err != nil {
		t.Fatalf("enable mkcert: %v", err)
	}
	info, err := svc.SwitchMode(context.Background(), "self-signed", "hermes.lan", nil, 30)
	if err != nil {
		t.Fatalf("switch back: %v", err)
	}

	if env.cfg.System.TLS.Mode != "self-signed" {
		t.Errorf("mode = %q, want self-signed", env.cfg.System.TLS.Mode)
	}
	if info.Issuer == "mkcert test CA" {
		t.Error("the mkcert certificate is still being served after switching back")
	}
}
