package services

import "testing"

// iproute2 nests the byte counters per direction; flat rx_bytes and
// tx_bytes keys decode to zero without an error.
func TestParseSitCountersReadsTheNestedStats(t *testing.T) {
	out := `[{"ifindex":5,"ifname":"sit1","stats64":{"rx":{"bytes":1234,"packets":10},"tx":{"bytes":5678,"packets":20}}}]`
	rx, tx, ok := parseSitCounters(out)
	if !ok || rx != 1234 || tx != 5678 {
		t.Errorf("parseSitCounters = %d, %d, %v; want 1234, 5678, true", rx, tx, ok)
	}
}
