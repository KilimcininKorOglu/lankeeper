package agent

import (
	"log"
	"sync"
	"time"
)

type Watchdog struct {
	mu       sync.Mutex
	timer    *time.Timer
	service  string
	onExpire func()
}

func NewWatchdog(service string, timeout time.Duration, onExpire func()) *Watchdog {
	w := &Watchdog{
		service:  service,
		onExpire: onExpire,
	}

	w.timer = time.AfterFunc(timeout, func() {
		log.Printf("watchdog expired for %s — executing rollback", service)
		if w.onExpire != nil {
			w.onExpire()
		}
	})

	return w
}

func (w *Watchdog) Confirm() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.timer != nil {
		w.timer.Stop()
		log.Printf("watchdog confirmed for %s", w.service)
	}
}

func (w *Watchdog) Cancel() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.timer != nil {
		w.timer.Stop()
	}
}
