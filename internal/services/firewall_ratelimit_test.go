package services

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// renderOne is a shorthand for the single-protocol case, which is what
// every assertion below needs.
func renderOne(t *testing.T, op config.OpenPort) string {
	t.Helper()
	lines, err := renderOpenPortRules(op)
	if err != nil {
		t.Fatalf("renderOpenPortRules(%+v): %v", op, err)
	}
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	return lines[0]
}

// TestOpenPortRendersItsRateLimit is the regression test. The shipped
// defaults carried a rate-limit map keyed by service name, it reached
// the template data, and the template never referenced it, so the
// configuration implied brute-force protection at the packet filter
// that did not exist. The limit now attaches to the only surface where
// the packet filter can act: a port the operator deliberately opened.
func TestOpenPortRendersItsRateLimit(t *testing.T) {
	line := renderOne(t, config.OpenPort{
		Name: "ssh-alt", Protocol: "tcp", Port: 2222, Enabled: true, RateLimit: "3/minute",
	})

	if !strings.Contains(line, "limit rate 3/minute") {
		t.Errorf("line carries no rate limit: %q", line)
	}
	// After `ct state new` so the budget covers new connections rather
	// than every packet of an established one.
	if !strings.Contains(line, "ct state new limit rate 3/minute accept") {
		t.Errorf("the limit is misplaced: %q", line)
	}
}

// TestOpenPortWithoutARateLimitIsUnchanged keeps every entry written
// before this field existed rendering exactly as it did.
func TestOpenPortWithoutARateLimitIsUnchanged(t *testing.T) {
	line := renderOne(t, config.OpenPort{
		Name: "http", Protocol: "tcp", Port: 80, Enabled: true,
	})

	want := "        tcp dport 80 ct state new accept # http"
	if line != want {
		t.Errorf("line = %q, want %q", line, want)
	}
}

// TestRateLimitCombinesWithASourceMatch checks the two optional pieces
// do not disturb each other.
func TestRateLimitCombinesWithASourceMatch(t *testing.T) {
	line := renderOne(t, config.OpenPort{
		Name: "vpn", Protocol: "udp", Port: 51820, Enabled: true,
		Source: "203.0.113.0/24", RateLimit: "10/second",
	})

	want := "        ip saddr 203.0.113.0/24 udp dport 51820 ct state new limit rate 10/second accept # vpn"
	if line != want {
		t.Errorf("line = %q, want %q", line, want)
	}
}

func TestRateLimitAppliesToBothProtocols(t *testing.T) {
	lines, err := renderOpenPortRules(config.OpenPort{
		Name: "svc", Protocol: "both", Port: 5000, Enabled: true, RateLimit: "5/hour",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for _, l := range lines {
		if !strings.Contains(l, "limit rate 5/hour") {
			t.Errorf("line missing the limit: %q", l)
		}
	}
}

func TestValidateOpenPortRateLimitAcceptsEveryUnit(t *testing.T) {
	for _, v := range []string{"1/second", "3/minute", "30/hour", "100/day", "999999/week", ""} {
		if err := ValidateOpenPortRateLimit(v); err != nil {
			t.Errorf("ValidateOpenPortRateLimit(%q) = %v, want nil", v, err)
		}
	}
}

// TestValidateOpenPortRateLimitRejectsInjection is the security half.
// The value is written straight into the ruleset, and nft parses the
// file as a whole, so a malformed rate does not fail its own line: it
// fails the entire load and takes every other rule with it.
func TestValidateOpenPortRateLimitRejectsInjection(t *testing.T) {
	bad := []string{
		"3/fortnight",
		"0/minute",
		"-1/minute",
		"abc",
		"3 / minute",
		"3/minute accept",
		"3/minute; drop",
		"3/minute\n        drop",
		"3/minute\"",
		"1234567/minute",
		"/minute",
		"3/",
	}
	for _, v := range bad {
		if err := ValidateOpenPortRateLimit(v); err == nil {
			t.Errorf("ValidateOpenPortRateLimit(%q) was accepted", v)
		}
	}
}

// TestRenderRefusesAnInjectedRateLimit confirms the validator is
// actually reached from the render path, not merely exported.
func TestRenderRefusesAnInjectedRateLimit(t *testing.T) {
	_, err := renderOpenPortRules(config.OpenPort{
		Name: "evil", Protocol: "tcp", Port: 22, Enabled: true,
		RateLimit: "3/minute\n        tcp dport 22 accept",
	})
	if err == nil {
		t.Fatal("an injected rate limit was rendered")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("got %v, want an error naming the rate limit", err)
	}
}

// TestBuildOpenPortRulesSkipsABadEntry keeps one rejected entry from
// taking the whole ruleset down with it, matching how the existing
// port and name validation behaves.
func TestBuildOpenPortRulesSkipsABadEntry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Firewall.OpenPorts = []config.OpenPort{
		{Name: "good", Protocol: "tcp", Port: 8080, Enabled: true, RateLimit: "5/minute"},
		{Name: "bad", Protocol: "tcp", Port: 9090, Enabled: true, RateLimit: "5/fortnight"},
	}
	svc := &FirewallService{cfg: cfg}

	lines := svc.buildOpenPortRules()
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "dport 8080") {
		t.Error("the valid entry was dropped along with the invalid one")
	}
	if strings.Contains(joined, "dport 9090") || strings.Contains(joined, "fortnight") {
		t.Errorf("the invalid entry reached the ruleset:\n%s", joined)
	}
}
