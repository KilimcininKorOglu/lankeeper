package services

import (
	"context"
	"log"
	"sync"
	"time"
)

// blocklistCheckInterval is how often the schedule is compared with the
// clock. Cron fields are minutes, so a finer tick buys nothing.
const blocklistCheckInterval = time.Minute

// blocklistUpdateTimeout bounds one scheduled download of every list.
const blocklistUpdateTimeout = 5 * time.Minute

// StartBlocklistSchedule refreshes the blocklist on
// DNS.BlocklistUpdateSchedule, in the router's local time, until ctx ends.
// The schedule is re-read on every tick, so an edit takes effect without
// a restart; an empty schedule turns the refresh off.
func (s *DNSService) StartBlocklistSchedule(ctx context.Context, wg *sync.WaitGroup) {
	wg.Go(func() {
		t := time.NewTicker(blocklistCheckInterval)
		defer t.Stop()
		var next time.Time
		var source string
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				next, source = s.blocklistTick(ctx, now, next, source)
			}
		}
	})
}

// blocklistTick runs the update when it is due and returns the next fire
// time and the schedule it came from.
func (s *DNSService) blocklistTick(ctx context.Context, now, next time.Time, source string) (time.Time, string) {
	s.mu.RLock()
	spec := s.cfg.DNS.BlocklistUpdateSchedule
	s.mu.RUnlock()
	if spec == "" {
		return time.Time{}, ""
	}
	sched, err := ParseSchedule(spec, time.Local)
	if err != nil {
		if spec != source {
			log.Printf("dns: blocklist schedule %q: %v", spec, err)
		}
		return time.Time{}, spec
	}
	if next.IsZero() || spec != source {
		return sched.Next(now), spec
	}
	if now.Before(next) {
		return next, spec
	}

	runCtx, cancel := context.WithTimeout(ctx, blocklistUpdateTimeout)
	defer cancel()
	if err := s.UpdateBlocklist(runCtx); err != nil {
		log.Printf("dns: scheduled blocklist update: %v", err)
	}
	return sched.Next(now), spec
}
