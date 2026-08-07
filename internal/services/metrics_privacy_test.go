package services

import (
	"strings"
	"testing"
)

// TestClientMetricsCarryNoHostname is the regression test. Per-client
// rows hashed the MAC and then emitted the hostname beside it in the
// clear. /metrics is deliberately unauthenticated because scrapers
// carry no session cookie, so any LAN device could pull a by-name
// inventory of its neighbours plus each one's live bandwidth from a
// single unauthenticated request. Hashing the MAC while publishing the
// hostname protects nothing: the hostname is the more identifying of
// the two.
func TestClientMetricsCarryNoHostname(t *testing.T) {
	snap := MetricsSnapshot{
		Clients: []ClientBandwidthMetric{
			{MAC: "abcdef01", RxBytes: 50, TxBytes: 60, RxBPS: 1, TxBPS: 2},
		},
	}

	var sb strings.Builder
	if err := snap.Write(&sb); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := sb.String()

	if !strings.Contains(out, "lankeeper_client_rx_bytes_total") {
		t.Fatal("the client family is absent; the test proves nothing")
	}
	if strings.Contains(out, "hostname") {
		t.Errorf("the exposition still carries a hostname label:\n%s", out)
	}
}

// TestClientLabelsHoldOnlyTheHashedMAC pins the label set itself, so a
// later change cannot reintroduce an identifying label without this
// failing.
func TestClientLabelsHoldOnlyTheHashedMAC(t *testing.T) {
	labels := clientLabels(ClientBandwidthMetric{MAC: "abcdef01"})

	if len(labels) != 1 {
		t.Fatalf("label set = %v, want exactly one label", labels)
	}
	if labels["mac"] != "abcdef01" {
		t.Errorf("mac label = %q, want the hashed value", labels["mac"])
	}
}

// TestMACIsStillHashedInTheCollector keeps the protection that was
// already there: the raw address must not reach the exposition either.
func TestMACIsStillHashedInTheCollector(t *testing.T) {
	const raw = "AA:BB:CC:DD:EE:FF"
	hashed := macHash(raw)

	if strings.Contains(hashed, ":") || len(hashed) != 8 {
		t.Fatalf("macHash(%q) = %q, want 8 hex characters", raw, hashed)
	}
	if strings.EqualFold(hashed, strings.ReplaceAll(raw, ":", "")) {
		t.Error("the hash returned the address unchanged")
	}
}
