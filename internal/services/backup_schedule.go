package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxBackupHistory caps the BackupConfig.History ring buffer.
// Long enough to cover ~7 weeks of daily backups before the oldest
// entries roll off, short enough that yaml round-trips stay cheap.
const MaxBackupHistory = 50

// Schedule represents a parsed cron-style backup schedule. We
// support the @hourly/@daily/@weekly/@monthly/@yearly aliases plus
// the standard 5-field "M H DOM Mo DOW" form with `*`, single
// values, comma lists, ranges (`a-b`) and step sliders (`*/k`).
// Named months/days and ranges-with-steps are not supported in v1.
type Schedule struct {
	minute     fieldSet // 0-59
	hour       fieldSet // 0-23
	dayOfMonth fieldSet // 1-31
	month      fieldSet // 1-12
	dayOfWeek  fieldSet // 0-6 (Sun=0)
	loc        *time.Location
}

// fieldSet is a 60-bit bitset carried in a uint64. The largest
// cron field is minute (0-59), well within 64 bits, so a single
// integer is enough for every field.
type fieldSet uint64

func (f fieldSet) has(n int) bool { return n >= 0 && n < 64 && f&(1<<n) != 0 }

func makeFieldSet(min, max int, all bool) fieldSet {
	var f fieldSet
	if all {
		for i := min; i <= max; i++ {
			f |= 1 << i
		}
	}
	return f
}

// ParseSchedule parses a cron expression in the project's reduced
// dialect. Returns ErrSchedule on malformed input. Empty spec
// yields a zero-value Schedule whose Next() always reports the
// far future, so the caller treats it as disabled.
func ParseSchedule(spec string, loc *time.Location) (*Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("empty schedule")
	}
	if loc == nil {
		loc = time.Local
	}

	// Aliases first.
	switch spec {
	case "@hourly":
		return parseFields("0 * * * *", loc)
	case "@daily", "@midnight":
		return parseFields("0 0 * * *", loc)
	case "@weekly":
		return parseFields("0 0 * * 0", loc)
	case "@monthly":
		return parseFields("0 0 1 * *", loc)
	case "@yearly", "@annually":
		return parseFields("0 0 1 1 *", loc)
	}
	return parseFields(spec, loc)
}

func parseFields(spec string, loc *time.Location) (*Schedule, error) {
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return nil, fmt.Errorf("schedule must have 5 fields, got %d", len(parts))
	}
	min, err := parseField(parts[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	hr, err := parseField(parts[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	dom, err := parseField(parts[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month: %w", err)
	}
	mo, err := parseField(parts[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	dow, err := parseField(parts[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("day-of-week: %w", err)
	}
	return &Schedule{
		minute:     min,
		hour:       hr,
		dayOfMonth: dom,
		month:      mo,
		dayOfWeek:  dow,
		loc:        loc,
	}, nil
}

func parseField(spec string, min, max int) (fieldSet, error) {
	if spec == "*" {
		return makeFieldSet(min, max, true), nil
	}
	var out fieldSet
	for _, atom := range strings.Split(spec, ",") {
		atom = strings.TrimSpace(atom)
		if atom == "" {
			continue
		}
		// Step form: */k or m-n/k (we only support */k in v1).
		step := 1
		if idx := strings.Index(atom, "/"); idx >= 0 {
			s, err := strconv.Atoi(atom[idx+1:])
			if err != nil || s < 1 {
				return 0, fmt.Errorf("bad step %q", atom)
			}
			step = s
			atom = atom[:idx]
		}
		var lo, hi int
		switch {
		case atom == "*":
			lo, hi = min, max
		case strings.Contains(atom, "-"):
			parts := strings.SplitN(atom, "-", 2)
			a, e1 := strconv.Atoi(parts[0])
			b, e2 := strconv.Atoi(parts[1])
			if e1 != nil || e2 != nil {
				return 0, fmt.Errorf("bad range %q", atom)
			}
			lo, hi = a, b
		default:
			n, err := strconv.Atoi(atom)
			if err != nil {
				return 0, fmt.Errorf("bad number %q", atom)
			}
			lo, hi = n, n
		}
		if lo < min || hi > max || lo > hi {
			return 0, fmt.Errorf("out-of-range field %d-%d (allowed %d-%d)", lo, hi, min, max)
		}
		for i := lo; i <= hi; i += step {
			out |= 1 << i
		}
	}
	if out == 0 {
		return 0, fmt.Errorf("empty field %q", spec)
	}
	return out, nil
}

// Next reports the next time at-or-after `after` that the schedule
// fires. We advance one minute at a time; the cap of 4 years is
// well beyond any reasonable cron, but bounds the loop so a
// pathologically-restrictive schedule (e.g. Feb 30) cannot hang
// the goroutine.
func (s *Schedule) Next(after time.Time) time.Time {
	if s == nil {
		return time.Time{}
	}
	t := after.In(s.loc).Add(time.Minute - time.Duration(after.Second())*time.Second).
		Truncate(time.Minute)
	cap := after.Add(4 * 365 * 24 * time.Hour)
	for t.Before(cap) {
		if s.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func (s *Schedule) matches(t time.Time) bool {
	if !s.minute.has(t.Minute()) {
		return false
	}
	if !s.hour.has(t.Hour()) {
		return false
	}
	if !s.month.has(int(t.Month())) {
		return false
	}
	// Cron quirk: if BOTH dayOfMonth and dayOfWeek are restricted,
	// either match suffices (Vixie semantics). When one is `*` only
	// the other matters.
	dom := s.dayOfMonth.has(t.Day())
	dow := s.dayOfWeek.has(int(t.Weekday()))
	dayOfMonthAll := s.dayOfMonth == makeFieldSet(1, 31, true)
	dayOfWeekAll := s.dayOfWeek == makeFieldSet(0, 6, true)
	switch {
	case dayOfMonthAll && dayOfWeekAll:
		return true
	case dayOfMonthAll:
		return dow
	case dayOfWeekAll:
		return dom
	default:
		return dom || dow
	}
}

// --- Scheduler -------------------------------------------------------

// scheduleMu guards the BackupService scheduler goroutine lifecycle.
// Declared at package scope rather than on the struct so we don't
// have to expose a Stop() — the ctx passed to StartScheduler is
// the only knob.
var scheduleMu sync.Mutex

// schedulerRunning indicates a scheduler goroutine is active. We
// don't try to support multiple parallel scheduler instances; a
// second StartScheduler call is a no-op.
var schedulerRunning bool

// StartScheduler launches the cron-driven backup goroutine. It
// computes the next run from cfg.Backup.Schedule on every tick and
// fires runBackup(ctx) when due. Schedule reloads happen on each
// tick so a config change takes effect by the next minute boundary.
// wg may be nil. When supplied, the goroutine is counted into it so a
// caller can wait for an in-flight run to finish: RunNow is called
// synchronously inside the loop below, so the goroutine does not exit
// until the current backup has recorded its history entry.
func (s *BackupService) StartScheduler(ctx context.Context, cfg *backupSchedulerConfig, wg *sync.WaitGroup) {
	scheduleMu.Lock()
	if schedulerRunning {
		scheduleMu.Unlock()
		return
	}
	schedulerRunning = true
	scheduleMu.Unlock()

	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			// The loop clears schedulerRunning inline before it
			// returns, so this deferred Done runs afterwards and a
			// returning Wait never sees the flag still set.
			defer wg.Done()
		}
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		var nextFire time.Time

		for {
			select {
			case <-ctx.Done():
				scheduleMu.Lock()
				schedulerRunning = false
				scheduleMu.Unlock()
				return
			case now := <-t.C:
				snap := cfg.Snapshot()
				if !snap.Enabled || snap.Schedule == "" {
					nextFire = time.Time{}
					continue
				}
				sched, err := ParseSchedule(snap.Schedule, snap.Location)
				if err != nil {
					log.Printf("backup scheduler: parse %q: %v", snap.Schedule, err)
					continue
				}
				if nextFire.IsZero() || nextFire.Before(snap.LastRun) {
					nextFire = sched.Next(now)
				}
				if !nextFire.IsZero() && !now.Before(nextFire) {
					if err := s.RunNow(ctx); err != nil {
						log.Printf("backup scheduler: run: %v", err)
					}
					nextFire = sched.Next(now.Add(time.Minute))
				}
			}
		}
	}()
}

// backupSchedulerConfig is the minimal slice of the live config the
// scheduler needs. Defined as an interface so the BackupService
// can be tested without dragging the whole config struct around.
type backupSchedulerConfig struct {
	provider func() backupSnapshot
}

func (b *backupSchedulerConfig) Snapshot() backupSnapshot {
	if b == nil || b.provider == nil {
		return backupSnapshot{}
	}
	return b.provider()
}

type backupSnapshot struct {
	Enabled  bool
	Schedule string
	Location *time.Location
	LastRun  time.Time
}

// trimHistory keeps the most-recent MaxBackupHistory entries.
// Caller passes the slice and gets the trimmed slice back.
func trimHistory(history []historyEntry) []historyEntry {
	if len(history) <= MaxBackupHistory {
		return history
	}
	return history[len(history)-MaxBackupHistory:]
}

// historyEntry is the unconfigured form of config.BackupHistory
// used internally before we copy fields into the YAML struct.
// Lives here so test code can poke at it without importing config.
type historyEntry struct {
	StartedAt   time.Time
	CompletedAt time.Time
	Bytes       int64
	Targets     []string
	Status      string
	Message     string
}

// sortHistoryByStartedAt sorts in ascending chronological order so
// the youngest entry sits at the end of the slice (ring buffer
// convention used elsewhere in the project).
func sortHistoryByStartedAt(entries []historyEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StartedAt.Before(entries[j].StartedAt)
	})
}

// RunNow performs a single end-to-end backup cycle: encrypted tar
// export + per-target upload + retention cleanup + history record.
// Defined here as a package-level method on BackupService so the
// scheduler goroutine has something to call; the actual upload
// implementations live in backup_local.go / backup_s3.go /
// backup_sftp.go and the orchestration body lives in
// backup_orchestration.go.
func (s *BackupService) RunNow(ctx context.Context) error {
	if s.runner == nil {
		return errors.New("backup runner not wired")
	}
	return s.runner(ctx)
}
