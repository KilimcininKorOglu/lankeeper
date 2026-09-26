package services

import (
	"strings"
	"testing"
)

// bpsDelta multiplies byte deltas by eight, so the rate series carry bits
// per second, and a dashboard built from the HELP text must read them so.
func TestClientRateHelpNamesBits(t *testing.T) {
	if got := bpsDelta(1000, 0, 1); got != 8000 {
		t.Fatalf("bpsDelta(1000 bytes, 1 s) = %v, want 8000 bits per second", got)
	}
	var b strings.Builder
	MetricsSnapshot{Clients: []ClientBandwidthMetric{{MAC: "aa:bb:cc:dd:ee:ff"}}}.writeClients(&b)
	for _, name := range []string{"lankeeper_client_rx_bps", "lankeeper_client_tx_bps"} {
		if !strings.Contains(b.String(), "# HELP "+name+" Instantaneous bits per second") {
			t.Errorf("%s HELP does not say bits per second:\n%s", name, b.String())
		}
	}
}
