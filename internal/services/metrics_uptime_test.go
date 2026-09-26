package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// The uptime series is the host's, which a crash-looping web process does
// not reset; the process start time is exported on its own so an alert can
// catch restarts.
func TestUptimeSeriesNameWhatTheyMeasure(t *testing.T) {
	snap := NewMetricsService(config.DefaultConfig(), nil, nil, nil, nil, nil, nil, nil, nil).collect(context.Background())
	var b strings.Builder
	snap.writeHost(&b)
	out := b.String()
	if !strings.Contains(out, "# HELP lankeeper_uptime_seconds Host uptime since boot.") {
		t.Errorf("uptime HELP does not name the host:\n%s", out)
	}
	var got float64
	for line := range strings.Lines(out) {
		if v, ok := strings.CutPrefix(line, "lankeeper_process_start_time_seconds "); ok {
			if _, err := fmt.Sscanf(v, "%g", &got); err != nil {
				t.Fatalf("parse %q: %v", v, err)
			}
		}
	}
	if int64(got) != processStart.Unix() {
		t.Errorf("process start time = %v, want %d", got, processStart.Unix())
	}
}
