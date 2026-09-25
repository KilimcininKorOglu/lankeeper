package services_test

import (
	"context"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

const chronycTracking = `Reference ID    : C0A80101 (time.example.org)
Stratum         : 3
Ref time (UTC)  : Thu Sep 25 10:00:00 2026
System time     : 0.000012345 seconds fast of NTP time
Last offset     : +0.000001234 seconds
Leap status     : Normal
`

// TestNTPStatusParsesTracking pins what GetStatus reads from
// `chronyc tracking`.
func TestNTPStatusParsesTracking(t *testing.T) {
	useStdoutAgent(t, map[string]string{"chronyc": chronycTracking})

	st, err := services.NewNTPService(&config.Config{}).GetStatus(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.RefSource != "time.example.org" || st.Stratum != 3 || st.Offset != "0.000012345 seconds" || !st.Synced {
		t.Errorf("status = %+v", *st)
	}
}
