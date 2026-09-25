package services_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

// fakeAgent records every exec.run / file.* call routed through
// netutil.SetAgentClient and returns canned successes. It lets cross-
// service integration tests drive the production code path (where
// netutil.Run goes via the agent UDS) without spawning an actual
// agent process.
type fakeAgent struct {
	mu       sync.Mutex
	execLog  []execCall
	writeLog []writeCall
}

type execCall struct {
	Cmd  string
	Args []string
}

type writeCall struct {
	Path string
	Body string
}

func (f *fakeAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "exec.run":
		return f.recordExec(params)
	case "file.write":
		return f.recordWrite(params)
	case "file.mkdir":
		return []byte(`{}`), nil
	case "file.read":
		return readPassthrough(params)
	}
	return nil, fmt.Errorf("fakeAgent: unhandled method %q", method)
}

// decodeParams round-trips params through JSON into v, so the fake does
// not depend on the unexported param types of the netutil package.
func decodeParams(params, v any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// recordExec logs one exec.run and answers an empty success.
func (f *fakeAgent) recordExec(params any) (json.RawMessage, error) {
	var p struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	f.execLog = append(f.execLog, execCall{Cmd: p.Cmd, Args: append([]string(nil), p.Args...)})
	return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
}

// recordWrite logs one file.write and mirrors it to the real filesystem,
// so a later file.read passthrough sees the bytes a production agent
// would have persisted. The mirror is best-effort: some tests target
// paths under /etc that the test process cannot create, and those
// tests assert through writeLog instead of reading the file back.
func (f *fakeAgent) recordWrite(params any) (json.RawMessage, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	f.writeLog = append(f.writeLog, writeCall{Path: p.Path, Body: p.Content})
	if dir := filepath.Dir(p.Path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	_ = os.WriteFile(p.Path, []byte(p.Content), 0o644)
	return []byte(`{}`), nil
}

// readPassthrough answers file.read from the real filesystem. The tests
// seed lease state with os.WriteFile, but IPv6Service reads it through
// netutil.ReadFile, which goes through the agent when one is set.
func readPassthrough(params any) (json.RawMessage, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(p.Path)
	if err != nil {
		return nil, fmt.Errorf("file.read passthrough: %w", err)
	}
	return json.Marshal(struct {
		Content string `json:"content"`
	}{Content: string(body)})
}

// lastWrite returns the body of the last file.write whose path ends in
// suffix, or "".
func (f *fakeAgent) lastWrite(suffix string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := ""
	for _, w := range f.writeLog {
		if strings.HasSuffix(w.Path, suffix) {
			body = w.Body
		}
	}
	return body
}

// nftModes reports whether an `nft -c -f` validate and an `nft -f`
// apply were run.
func (f *fakeAgent) nftModes() (validate, apply bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.execLog {
		if c.Cmd != "nft" {
			continue
		}
		flat := strings.Join(c.Args, " ")
		if strings.Contains(flat, "-c -f") {
			validate = true
		} else if strings.HasPrefix(flat, "-f ") {
			apply = true
		}
	}
	return validate, apply
}

// writeLeaseAtomically writes a dhcp6c lease for prefix/56 the way the
// hook script does: a temp file renamed into place.
func writeLeaseAtomically(t *testing.T, statePath, prefix string) {
	t.Helper()
	tmp := statePath + ".tmp"
	body := []byte(fmt.Sprintf(
		`{"timestamp":%d,"reason":"REPLY","prefix":%q,"prefixLength":56,"preferredLifetime":3600,"validLifetime":7200,"rdnss":"2001:4860:4860::8888 2001:4860:4860::8844"}`,
		time.Now().Unix(), prefix))
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		t.Fatalf("write tmp lease: %v", err)
	}
	if err := os.Rename(tmp, statePath); err != nil {
		t.Fatalf("rename lease: %v", err)
	}
}

// waitForHits polls until counter reaches n or the timeout passes, and
// reports whether it did.
func waitForHits(counter *atomic.Int32, n int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for counter.Load() < n && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	return counter.Load() >= n
}

func (f *fakeAgent) execCount(cmd string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.execLog {
		if c.Cmd == cmd {
			n++
		}
	}
	return n
}

// execCallsCopy returns a snapshot of the recorded exec.run calls so
// callers can inspect args without holding the agent lock.
func (f *fakeAgent) execCallsCopy() []execCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]execCall, len(f.execLog))
	copy(out, f.execLog)
	return out
}

func (f *fakeAgent) wroteFile(suffix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.writeLog {
		if strings.HasSuffix(w.Path, suffix) {
			return true
		}
	}
	return false
}

// TestIPv6LeaseTriggersFirewallApply wires a real IPv6Service and a
// real FirewallService together exactly like web/server.go does in
// production: the IPv6 lease watcher's callback invokes Apply +
// Confirm on the firewall. We then simulate a dhcp6c lease event by
// atomically renaming a JSON file into place and assert that:
//
//  1. The IPv6 service refreshed the dnsmasq RA drop-in (file.write
//     to /etc/dnsmasq.d/lankeeper-ipv6-ra.conf via the fake agent).
//  2. dnsmasq was reload-or-restarted via systemctl.
//  3. The firewall ran its full Apply chain (nft list ruleset for
//     snapshot, nft -c -f for validation, nft -f for apply).
//  4. The user callback observed the new lease.
//
// This is the cross-service contract that ipv6_test.go cannot cover
// because it uses a mock callback. It's the only integration safety
// net for the lease-driven firewall refresh feature shipped in v0.3.0.
func TestIPv6LeaseTriggersFirewallApply(t *testing.T) {
	// netutil.agentClient is a process-global, so this test cannot run
	// in parallel with anything that touches netutil.Run. Reset on exit.
	agent := &fakeAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	cfg := newIPv6TestConfig(t)
	cfg.SetFilePath(filepath.Join(t.TempDir(), "router.yaml"))
	cfg.System.WebPort = 8443
	cfg.Firewall.DefaultPolicy = "drop"
	cfg.IPv6.Enabled = "auto"
	cfg.IPv6.WAN.RequestPrefix = true

	ipv6 := newIPv6TestService(t, cfg)
	statePath := filepath.Join(t.TempDir(), "ipv6-prefix.json")
	ipv6.SetStatePathForTest(statePath)

	fw, err := services.NewFirewallServiceFromFS(cfg, testNftTemplate)
	if err != nil {
		t.Fatalf("firewall service: %v", err)
	}

	// Wire the cross-service callback the same way web/server.go does:
	// every lease event re-applies the firewall and immediately
	// confirms (lease arrival itself is connectivity proof, the 30s
	// watchdog is redundant).
	var callbackHits atomic.Int32
	ipv6.SetOnLeaseChange(func(ctx context.Context, _ services.PrefixState) error {
		callbackHits.Add(1)
		if applyErr := fw.Apply(ctx); applyErr != nil {
			return applyErr
		}
		fw.Confirm()
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ipv6.StartLeaseWatcher(ctx, nil); err != nil {
		t.Fatalf("StartLeaseWatcher: %v", err)
	}
	defer ipv6.StopLeaseWatcher()

	// Simulate the dhcp6c hook script's atomic-mv lease write.
	writeLeaseAtomically(t, statePath, "2001:db8:abcd::")

	// Wait until the watcher dispatches the lease event. The initial
	// dispatch (before any file exists) silently fails Status() and
	// never reaches the callback, so the first hit corresponds to our
	// atomic-mv write.
	if !waitForHits(&callbackHits, 1, 3*time.Second) {
		t.Fatalf("expected callback to fire after lease write, got %d hits", callbackHits.Load())
	}

	// callbackHits is incremented at the top of the callback, so seeing
	// it says the dispatch started, not that it finished. Reading the
	// agent's logs while fw.Apply is still issuing nft commands is a
	// race, and it reports zero invocations whenever it loses. Stop the
	// watcher first: it waits for the dispatch to complete, so
	// everything below observes a settled state. Idempotent, so the
	// deferred stop above stays harmless.
	ipv6.StopLeaseWatcher()

	// dnsmasq RA drop-in must have been written and a reload issued.
	if !agent.wroteFile("/etc/dnsmasq.d/lankeeper-ipv6-ra.conf") {
		t.Errorf("expected dnsmasq RA drop-in write, write log: %+v", agent.writeLog)
	}
	if agent.execCount("systemctl") < 1 {
		t.Errorf("expected at least one systemctl reload-or-restart for dnsmasq, exec log: %+v", agent.execLog)
	}

	// FirewallService.Apply chain: snapshot (nft list ruleset),
	// validate (nft -c -f), apply (nft -f). Each goes through
	// netutil.Run and therefore through the fake agent.
	if got := agent.execCount("nft"); got < 3 {
		t.Errorf("expected at least 3 nft invocations (snapshot, validate, apply), got %d; exec log: %+v",
			got, agent.execLog)
	}

	// Spot-check: confirm we actually saw the validate flag, not just
	// three random nft calls. This is the cross-service contract.
	if sawValidate, sawApply := agent.nftModes(); !sawValidate || !sawApply {
		t.Errorf("missing nft validate/apply (validate=%v apply=%v); exec log: %+v",
			sawValidate, sawApply, agent.execLog)
	}
}
