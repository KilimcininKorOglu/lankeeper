package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// pingAgent answers ping either way, so a probe can be driven without a
// network. hosts holds the addresses that are reachable; everything else
// fails with a message the caller should preserve.
type pingAgent struct {
	reachable map[string]bool
}

func (a *pingAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "exec.run" {
		return []byte(`{}`), nil
	}
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
	host := ""
	if len(p.Args) > 0 {
		host = p.Args[len(p.Args)-1]
	}
	if a.reachable[host] {
		return []byte(`{"stdout":"1 received","stderr":"","exitCode":0}`), nil
	}
	return nil, errors.New("100% packet loss")
}

// useAgent installs a fake for the process-global client. Mutating that
// global means no t.Parallel in any test here.
func useAgent(t *testing.T, a netutil.AgentCaller) {
	t.Helper()
	netutil.SetAgentClient(a)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
}

// TestAFailedProbeKeepsTheTargetAndTheError is the regression test.
// probeTargets reduced each probe to ok := err == nil and then the whole
// check to one bool, so the only record of a failure was the check name
// and a counter. The shipped wan-internet check probes two independent
// hosts, and once the counter crosses its threshold the remediation
// bounces the interface or reboots the router.
func TestAFailedProbeKeepsTheTargetAndTheError(t *testing.T) {
	useAgent(t, &pingAgent{})

	svc := NewHealthCheckService(&config.Config{})
	targets := []config.HealthCheckTarget{
		{Type: "ping", Host: "1.1.1.1"},
		{Type: "ping", Host: "8.8.8.8"},
	}

	ok, failures := svc.probeTargets(context.Background(), targets, time.Second)
	if ok {
		t.Fatal("the probe reported success with no reachable target")
	}
	if len(failures) != 2 {
		t.Fatalf("recorded %d failures, want one per target", len(failures))
	}

	rendered := describeFailures(failures)
	for _, host := range []string{"1.1.1.1", "8.8.8.8"} {
		if !strings.Contains(rendered, host) {
			t.Errorf("the failure text does not name %s: %s", host, rendered)
		}
	}
	if !strings.Contains(rendered, "packet loss") {
		t.Errorf("the failure text does not carry the probe error: %s", rendered)
	}
}

// TestOneReachableTargetEndsTheProbe keeps the short-circuit: the check
// asks whether anything answered, so the first success is the answer and
// the earlier failures are not a problem to report.
func TestOneReachableTargetEndsTheProbe(t *testing.T) {
	useAgent(t, &pingAgent{reachable: map[string]bool{"8.8.8.8": true}})

	svc := NewHealthCheckService(&config.Config{})
	targets := []config.HealthCheckTarget{
		{Type: "ping", Host: "1.1.1.1"},
		{Type: "ping", Host: "8.8.8.8"},
	}

	ok, failures := svc.probeTargets(context.Background(), targets, time.Second)
	if !ok {
		t.Fatal("a reachable target did not report success")
	}
	if len(failures) != 0 {
		t.Errorf("a successful check carried %d failures", len(failures))
	}
}

// TestAnUnsupportedTargetTypeSaysSo covers the branch that used to fall
// through the switch untouched: ok stayed false with no error, so a
// misspelled type read exactly like an unreachable host and would have
// driven the remediation for ever.
func TestAnUnsupportedTargetTypeSaysSo(t *testing.T) {
	useAgent(t, &pingAgent{})

	svc := NewHealthCheckService(&config.Config{})
	targets := []config.HealthCheckTarget{{Type: "pnig", Host: "1.1.1.1"}}

	ok, failures := svc.probeTargets(context.Background(), targets, time.Second)
	if ok {
		t.Fatal("an unsupported target type reported success")
	}
	if len(failures) != 1 {
		t.Fatalf("recorded %d failures, want 1", len(failures))
	}
	if got := describeFailures(failures); !strings.Contains(got, "unsupported target type") {
		t.Errorf("the failure does not explain the unknown type: %s", got)
	}
}

// TestAnHTTPProbeReportsWhyItFailed covers the other probe kind. A
// rejected URL used to be logged inside httpProbe with no reference to
// the check it belonged to, and everything else collapsed to false.
func TestAnHTTPProbeReportsWhyItFailed(t *testing.T) {
	svc := NewHealthCheckService(&config.Config{})
	targets := []config.HealthCheckTarget{
		{Type: "http", URL: "http://127.0.0.1:9/probe", ExpectStatus: 204},
	}

	ok, failures := svc.probeTargets(context.Background(), targets, time.Second)
	if ok {
		t.Fatal("a probe against a refused address reported success")
	}
	got := describeFailures(failures)
	if !strings.Contains(got, "http://127.0.0.1:9/probe") {
		t.Errorf("the failure does not name the URL: %s", got)
	}
	if strings.HasSuffix(strings.TrimSpace(got), "<nil>") {
		t.Errorf("the failure carries no error: %s", got)
	}
}

// TestTheCheckLogNamesEveryTarget is the end-to-end check on what the
// operator actually reads. One line has to answer what was tried and
// what each target said, or a WAN outage and an upstream that filters
// ICMP are indistinguishable after the fact.
//
// log output is process-global, so no t.Parallel here.
func TestTheCheckLogNamesEveryTarget(t *testing.T) {
	useAgent(t, &pingAgent{})

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	svc := NewHealthCheckService(&config.Config{})
	check := config.HealthCheckEntry{
		Name:             "wan-internet",
		FailureThreshold: 3,
		Targets: []config.HealthCheckTarget{
			{Type: "ping", Host: "1.1.1.1"},
			{Type: "ping", Host: "8.8.8.8"},
		},
	}

	svc.executeCheck(context.Background(), check, time.Second, time.Minute)

	logged := buf.String()
	if !strings.Contains(logged, "wan-internet") {
		t.Fatalf("the check name is missing from the log: %q", logged)
	}
	if !strings.Contains(logged, "failure 1/3") {
		t.Errorf("the counter is missing from the log: %q", logged)
	}
	for _, host := range []string{"1.1.1.1", "8.8.8.8"} {
		if !strings.Contains(logged, host) {
			t.Errorf("the log does not name %s: %q", host, logged)
		}
	}
}

// TestASuccessfulCheckLogsNothing keeps the detail from becoming noise:
// a working WAN must not write a line every interval.
func TestASuccessfulCheckLogsNothing(t *testing.T) {
	useAgent(t, &pingAgent{reachable: map[string]bool{"1.1.1.1": true}})

	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	svc := NewHealthCheckService(&config.Config{})
	check := config.HealthCheckEntry{
		Name:             "wan-internet",
		FailureThreshold: 3,
		Targets:          []config.HealthCheckTarget{{Type: "ping", Host: "1.1.1.1"}},
	}

	svc.executeCheck(context.Background(), check, time.Second, time.Minute)

	if got := buf.String(); got != "" {
		t.Errorf("a passing check wrote to the log: %q", got)
	}
}

// TestACheckWithNoTargetsSaysThat covers the degenerate config, which
// otherwise logs a failure with an empty explanation.
func TestACheckWithNoTargetsSaysThat(t *testing.T) {
	svc := NewHealthCheckService(&config.Config{})

	ok, failures := svc.probeTargets(context.Background(), nil, time.Second)
	if ok {
		t.Fatal("a check with no targets reported success")
	}
	if got := describeFailures(failures); !strings.Contains(got, "no targets") {
		t.Errorf("describeFailures = %q, want it to say there were none", got)
	}
}
