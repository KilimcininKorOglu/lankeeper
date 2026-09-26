package services

import "testing"

// chrony 4 prints a column header before the separator. It must not be
// listed as a source, and the state is the second character of the
// first column, not the mode.
func TestParseChronySourcesReadsChrony4Output(t *testing.T) {
	out := `MS Name/IP address         Stratum Poll Reach LastRx Last sample
===============================================================================
^* time.cloudflare.com           3   6   377    35   +123us[ +145us] +/-   12ms
^+ ntp1.example.org              2   6   377    34  -1234us[-1212us] +/-   20ms
^? 192.0.2.1                     0   6     0     -     +0ns[   +0ns] +/-    0ns
`
	got := parseChronySources(out)
	want := []NTPSource{
		{State: "*", Name: "time.cloudflare.com", Stratum: 3, Poll: "6", Offset: "+123us"},
		{State: "+", Name: "ntp1.example.org", Stratum: 2, Poll: "6", Offset: "-1234us"},
		{State: "?", Name: "192.0.2.1", Stratum: 0, Poll: "6", Offset: "+0ns"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d sources, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("source %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
