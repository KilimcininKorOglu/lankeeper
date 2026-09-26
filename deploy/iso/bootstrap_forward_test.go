package iso_test

import (
	"os"
	"regexp"
	"testing"
)

// forwardChain captures the bootstrap ruleset's forward chain body.
var forwardChain = regexp.MustCompile(`(?s)chain forward \{(.*?)\n\s*\}`)

// Both installers turn on IPv4 and IPv6 forwarding, and the bootstrap
// ruleset is what nftables loads until the live one is confirmed, so its
// forward chain must drop by default rather than route between every
// interface, WAN included.
func TestBootstrapRulesetDropsForwardedTraffic(t *testing.T) {
	for _, path := range []string{"../install.sh", "post-install.sh"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		m := forwardChain.FindSubmatch(raw)
		if m == nil {
			t.Fatalf("%s: no bootstrap forward chain found; re-derive this check", path)
		}
		if !regexp.MustCompile(`policy drop;`).Match(m[1]) {
			t.Errorf("%s: the bootstrap forward chain does not drop by default:%s", path, m[1])
		}
	}
}
