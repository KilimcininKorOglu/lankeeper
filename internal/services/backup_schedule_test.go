package services

import (
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation %s: %v", name, err)
	}
	return loc
}

func TestParseScheduleAliases(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		spec    string
		probe   time.Time
		want    time.Time
		wantErr bool
	}{
		{"@hourly", time.Date(2026, 5, 7, 12, 30, 0, 0, utc), time.Date(2026, 5, 7, 13, 0, 0, 0, utc), false},
		{"@daily", time.Date(2026, 5, 7, 12, 30, 0, 0, utc), time.Date(2026, 5, 8, 0, 0, 0, 0, utc), false},
		{"@weekly", time.Date(2026, 5, 7, 12, 30, 0, 0, utc), time.Date(2026, 5, 10, 0, 0, 0, 0, utc), false}, // next Sunday
		{"@monthly", time.Date(2026, 5, 7, 12, 30, 0, 0, utc), time.Date(2026, 6, 1, 0, 0, 0, 0, utc), false},
		{"@yearly", time.Date(2026, 5, 7, 12, 30, 0, 0, utc), time.Date(2027, 1, 1, 0, 0, 0, 0, utc), false},
		{"", time.Time{}, time.Time{}, true},
		{"bogus", time.Time{}, time.Time{}, true},
	}
	for _, tc := range cases {
		got, err := ParseSchedule(tc.spec, utc)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %v", tc.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.spec, err)
			continue
		}
		if next := got.Next(tc.probe); !next.Equal(tc.want) {
			t.Errorf("%q.Next(%s) = %s, want %s", tc.spec, tc.probe, next, tc.want)
		}
	}
}

func TestParseFieldsBasic(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		spec    string
		probe   time.Time
		want    time.Time
		wantErr bool
	}{
		{"0 3 * * *", time.Date(2026, 5, 7, 1, 0, 0, 0, utc), time.Date(2026, 5, 7, 3, 0, 0, 0, utc), false},
		{"0 3 * * *", time.Date(2026, 5, 7, 4, 0, 0, 0, utc), time.Date(2026, 5, 8, 3, 0, 0, 0, utc), false},
		{"*/15 * * * *", time.Date(2026, 5, 7, 12, 7, 0, 0, utc), time.Date(2026, 5, 7, 12, 15, 0, 0, utc), false},
		{"0 9-17 * * 1-5", time.Date(2026, 5, 7, 8, 30, 0, 0, utc), time.Date(2026, 5, 7, 9, 0, 0, 0, utc), false},
		// day-of-month + day-of-week both set: either matches.
		{"0 0 1 * 0", time.Date(2026, 5, 2, 0, 0, 0, 0, utc), time.Date(2026, 5, 3, 0, 0, 0, 0, utc), false}, // Sunday
		// invalid:
		{"60 0 * * *", time.Time{}, time.Time{}, true}, // minute out of range
		{"0 24 * * *", time.Time{}, time.Time{}, true}, // hour out of range
		{"0 0 * *", time.Time{}, time.Time{}, true},    // not 5 fields
		{"0 0 * 13 *", time.Time{}, time.Time{}, true}, // month out of range
		{"a b c d e", time.Time{}, time.Time{}, true},
	}
	for _, tc := range cases {
		got, err := ParseSchedule(tc.spec, utc)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", tc.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.spec, err)
			continue
		}
		if next := got.Next(tc.probe); !next.Equal(tc.want) {
			t.Errorf("%q.Next(%s) = %s, want %s", tc.spec, tc.probe, next, tc.want)
		}
	}
}

func TestNextRespectsTimezone(t *testing.T) {
	istanbul := mustLoc(t, "Europe/Istanbul")
	s, err := ParseSchedule("0 3 * * *", istanbul)
	if err != nil {
		t.Fatal(err)
	}
	// Probe at 2026-05-07 02:30 UTC = 05:30 Istanbul → already past 03:00 local,
	// so next fire is 2026-05-08 03:00 Istanbul = 2026-05-08 00:00 UTC.
	probe := time.Date(2026, 5, 7, 2, 30, 0, 0, time.UTC)
	got := s.Next(probe)
	wantUTC := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	if !got.UTC().Equal(wantUTC) {
		t.Errorf("Next() = %s (UTC), want %s", got.UTC(), wantUTC)
	}
}

func TestParseFieldsListAndRange(t *testing.T) {
	s, err := ParseSchedule("5,15,25 * * * *", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	probe := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	steps := []int{5, 15, 25}
	for _, want := range steps {
		got := s.Next(probe)
		if got.Minute() != want {
			t.Errorf("expected minute %d, got %s", want, got)
		}
		probe = got.Add(time.Minute)
	}
}

func TestParseFieldErrorMessages(t *testing.T) {
	cases := []struct {
		spec        string
		wantContain string
	}{
		{"0 0 32 * *", "out-of-range"},
		{"0 0 * * 9", "out-of-range"},
		{"*/0 * * * *", "bad step"},
	}
	for _, tc := range cases {
		_, err := ParseSchedule(tc.spec, time.UTC)
		if err == nil {
			t.Errorf("%q: expected error", tc.spec)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantContain) {
			t.Errorf("%q: error %v does not contain %q", tc.spec, err, tc.wantContain)
		}
	}
}

func TestTrimHistoryRingBuffer(t *testing.T) {
	var entries []historyEntry
	for i := range MaxBackupHistory + 10 {
		entries = append(entries, historyEntry{
			StartedAt: time.Unix(int64(i), 0),
			Status:    "ok",
		})
	}
	trimmed := trimHistory(entries)
	if len(trimmed) != MaxBackupHistory {
		t.Fatalf("trim length = %d, want %d", len(trimmed), MaxBackupHistory)
	}
	if trimmed[0].StartedAt.Unix() != 10 {
		t.Errorf("oldest after trim = %d, want 10", trimmed[0].StartedAt.Unix())
	}
}

func TestSortHistoryByStartedAt(t *testing.T) {
	entries := []historyEntry{
		{StartedAt: time.Unix(300, 0)},
		{StartedAt: time.Unix(100, 0)},
		{StartedAt: time.Unix(200, 0)},
	}
	sortHistoryByStartedAt(entries)
	for i := 0; i < len(entries)-1; i++ {
		if entries[i].StartedAt.After(entries[i+1].StartedAt) {
			t.Errorf("not sorted at index %d", i)
		}
	}
}
