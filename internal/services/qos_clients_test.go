package services

import (
	"fmt"
	"strings"
	"testing"
)

func TestCounterIDStableAndCaseInsensitive(t *testing.T) {
	a := counterID("AA:BB:CC:DD:EE:FF")
	b := counterID("aa-bb-cc-dd-ee-ff")
	c := counterID("aabbccddeeff")
	if a != b || b != c {
		t.Fatalf("counterID should ignore case/separators: %q %q %q", a, b, c)
	}
	if len(a) != 8 {
		t.Fatalf("counterID should be 8 hex chars, got %d (%q)", len(a), a)
	}
}

func TestDedupAndCapNormalisesAndCaps(t *testing.T) {
	in := []string{
		"AA:BB:CC:DD:EE:01",
		"  aa:bb:cc:dd:ee:01  ", // dup with whitespace
		"AA:BB:CC:DD:EE:02",
		"",
	}
	got := dedupAndCap(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique macs, got %d (%v)", len(got), got)
	}
	for _, m := range got {
		if m != strings.ToLower(m) {
			t.Errorf("expected lowercase mac, got %q", m)
		}
	}

	// Cap test.
	huge := make([]string, MaxQoSClients+10)
	for i := range huge {
		huge[i] = generateMAC(i)
	}
	capped := dedupAndCap(huge)
	if len(capped) != MaxQoSClients {
		t.Fatalf("expected cap at %d, got %d", MaxQoSClients, len(capped))
	}
}

func generateMAC(i int) string {
	// Encode the index as the lower 24 bits of the MAC so each i
	// produces a unique address within the test range.
	return fmt.Sprintf("aa:bb:cc:%02x:%02x:%02x", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
}

func TestRenderQoSTableShape(t *testing.T) {
	macs := []string{"aa:bb:cc:dd:ee:01"}
	got := renderQoSTable(macs, map[string]string{"aa:bb:cc:dd:ee:01": "10.10.10.50"})
	if !strings.Contains(got, "table inet "+qosTableName) {
		t.Errorf("missing table declaration: %s", got)
	}
	// A routed download does not carry the client MAC as its received
	// destination, so it is counted by the leased address.
	inName0, _ := counterNames("aa:bb:cc:dd:ee:01")
	if !strings.Contains(got, "ip daddr 10.10.10.50 counter name "+inName0) {
		t.Errorf("missing download rule on the leased address: %s", got)
	}
	if strings.Contains(got, "ether daddr") {
		t.Errorf("download still matched on ether daddr: %s", got)
	}
	if !strings.Contains(got, "ether saddr aa:bb:cc:dd:ee:01") {
		t.Errorf("missing egress rule: %s", got)
	}
	inName, outName := counterNames("aa:bb:cc:dd:ee:01")
	if !strings.Contains(got, "counter "+inName) || !strings.Contains(got, "counter "+outName) {
		t.Errorf("missing counters %q/%q in: %s", inName, outName, got)
	}
}

func TestParseQoSCountersFiltersByPrefix(t *testing.T) {
	raw := []byte(`{
	  "nftables": [
	    {"metainfo":{"version":"1.0.6"}},
	    {"counter":{"family":"inet","table":"lankeeper_qos","name":"cli_aabbccdd_in","packets":10,"bytes":1500}},
	    {"counter":{"family":"inet","table":"lankeeper_qos","name":"cli_aabbccdd_out","packets":20,"bytes":3000}},
	    {"counter":{"family":"inet","table":"lankeeper_qos","name":"otherCounter","packets":99,"bytes":99}},
	    {"chain":{"family":"inet","table":"lankeeper_qos","name":"fwd"}}
	  ]
	}`)
	out, err := parseQoSCounters(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 cli_ counters, got %d (%v)", len(out), out)
	}
	if out["cli_aabbccdd_in"].Bytes != 1500 {
		t.Errorf("wrong bytes for in counter: %v", out["cli_aabbccdd_in"])
	}
	if _, ok := out["otherCounter"]; ok {
		t.Errorf("otherCounter should have been filtered out")
	}
}

func TestParseQoSCountersEmpty(t *testing.T) {
	out, err := parseQoSCounters(nil)
	if err != nil {
		t.Fatalf("nil input should not error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("nil input should yield empty map, got %d", len(out))
	}
}

func TestBpsDeltaResetGuard(t *testing.T) {
	// Counter reset (curr < prev) should yield zero, not wrap.
	if got := bpsDelta(100, 1000, 1.0); got != 0 {
		t.Errorf("reset guard failed: got %d", got)
	}
	// Normal forward delta: 1000 bytes over 1s = 8000 bps.
	if got := bpsDelta(2000, 1000, 1.0); got != 8000 {
		t.Errorf("expected 8000bps, got %d", got)
	}
	// Zero interval guard.
	if got := bpsDelta(2000, 1000, 0); got != 0 {
		t.Errorf("zero interval should yield 0, got %d", got)
	}
}

// A lease address reaches the nft script, so anything that is not an
// IPv4 address must not produce a rule.
func TestRenderQoSTableSkipsAnInvalidLeaseAddress(t *testing.T) {
	got := renderQoSTable([]string{"aa:bb:cc:dd:ee:02"}, map[string]string{"aa:bb:cc:dd:ee:02": "10.0.0.1 accept\n"})
	if strings.Contains(got, "ip daddr") {
		t.Fatalf("an invalid lease address produced a rule: %s", got)
	}
}
