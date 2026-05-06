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
		// Decode the params struct via JSON so we don't depend on the
		// unexported execParams type from the netutil package.
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		var p struct {
			Cmd  string   `json:"cmd"`
			Args []string `json:"args"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		f.execLog = append(f.execLog, execCall{Cmd: p.Cmd, Args: append([]string(nil), p.Args...)})
		// Return an empty successful ExecResult.
		return []byte(`{"stdout":"","stderr":"","exitCode":0}`), nil
	case "file.write":
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		f.writeLog = append(f.writeLog, writeCall{Path: p.Path, Body: p.Content})
		// Mirror the write to the real filesystem so a subsequent
		// file.read passthrough sees the same bytes a production
		// agent would have persisted. Best-effort: failures are
		// silent because some tests target paths under /etc which
		// the test process cannot create. Those tests assert via
		// writeLog instead of round-tripping through file.read.
		if dir := filepath.Dir(p.Path); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		_ = os.WriteFile(p.Path, []byte(p.Content), 0o644)
		return []byte(`{}`), nil
	case "file.mkdir":
		return []byte(`{}`), nil
	case "file.read":
		// The test seeds the lease state via os.WriteFile directly, but
		// IPv6Service.rdnssAddrs reads it via netutil.ReadFile which
		// goes through the agent when one is set. Pass through to the
		// real filesystem so the rendered RA actually sees the JSON.
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		body, err := os.ReadFile(p.Path)
		if err != nil {
			return nil, fmt.Errorf("file.read passthrough: %w", err)
		}
		out, _ := json.Marshal(struct {
			Content string `json:"content"`
		}{Content: string(body)})
		return out, nil
	}
	return nil, fmt.Errorf("fakeAgent: unhandled method %q", method)
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
	if err := ipv6.StartLeaseWatcher(ctx); err != nil {
		t.Fatalf("StartLeaseWatcher: %v", err)
	}
	defer ipv6.StopLeaseWatcher()

	// Simulate the dhcp6c hook script's atomic-mv lease write.
	tmp := statePath + ".tmp"
	body := []byte(fmt.Sprintf(
		`{"timestamp":%d,"reason":"REPLY","prefix":"2001:db8:abcd::","prefixLength":56,"preferredLifetime":3600,"validLifetime":7200,"rdnss":"2001:4860:4860::8888 2001:4860:4860::8844"}`,
		time.Now().Unix()))
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		t.Fatalf("write tmp lease: %v", err)
	}
	if err := os.Rename(tmp, statePath); err != nil {
		t.Fatalf("rename lease: %v", err)
	}

	// Wait until the watcher dispatches the lease event. The initial
	// dispatch (before any file exists) silently fails Status() and
	// never reaches the callback, so the first hit corresponds to our
	// atomic-mv write.
	deadline := time.Now().Add(3 * time.Second)
	for callbackHits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if callbackHits.Load() < 1 {
		t.Fatalf("expected callback to fire after lease write, got %d hits", callbackHits.Load())
	}

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
	sawValidate := false
	sawApply := false
	for _, c := range agent.execLog {
		if c.Cmd != "nft" {
			continue
		}
		flat := strings.Join(c.Args, " ")
		if strings.Contains(flat, "-c -f") {
			sawValidate = true
		} else if strings.HasPrefix(flat, "-f ") {
			sawApply = true
		}
	}
	if !sawValidate || !sawApply {
		t.Errorf("missing nft validate/apply (validate=%v apply=%v); exec log: %+v",
			sawValidate, sawApply, agent.execLog)
	}
}
