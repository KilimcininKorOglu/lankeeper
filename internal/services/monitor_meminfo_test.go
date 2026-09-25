package services

import (
	"strings"
	"testing"
)

// TestParseMemInfoRefusesAMissingMemAvailable is the regression test.
// A read that ended before MemAvailable, or a kernel that omits it, left
// the field at zero, and the monitor reported every byte of memory as
// used. The scanner error was never checked either.
func TestParseMemInfoRefusesAMissingMemAvailable(t *testing.T) {
	if _, _, _, err := parseMemInfo(strings.NewReader("MemTotal:        4000000 kB\nMemFree:  100 kB\n")); err == nil {
		t.Error("meminfo without MemAvailable was accepted")
	}

	total, used, percent, err := parseMemInfo(strings.NewReader(
		"MemTotal:        4000000 kB\nMemFree:          500000 kB\nMemAvailable:    3000000 kB\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if total != 4000000*1024 || used != 1000000*1024 || percent != 25 {
		t.Errorf("total=%d used=%d percent=%v, want 4096000000 1024000000 25", total, used, percent)
	}
}
