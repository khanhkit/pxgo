package guardian

import "time"

type Watchdog struct {
	hangWindow   time.Duration
	ready        bool
	lastProgress time.Time
	lastObserver time.Time
	lastSequence uint64
}

func NewWatchdog(hangWindow time.Duration) *Watchdog {
	if hangWindow <= 0 {
		hangWindow = 20 * time.Second
	}
	return &Watchdog{hangWindow: hangWindow}
}

func (w *Watchdog) Ready(now time.Time) {
	w.ready = true
	w.lastProgress = now
	w.lastObserver = now
	w.lastSequence = 0
}

func (w *Watchdog) Beat(sequence uint64, now time.Time) {
	if !w.ready || sequence <= w.lastSequence {
		return
	}
	w.lastSequence = sequence
	w.lastProgress = now
}

// Check reports a worker hang. A large gap in the parent observer itself is
// treated as a suspend/scheduler pause and grants one fresh grace window.
func (w *Watchdog) Check(now time.Time) bool {
	if !w.ready {
		w.lastObserver = now
		return false
	}
	if !w.lastObserver.IsZero() && now.Sub(w.lastObserver) > w.hangWindow {
		w.lastObserver = now
		w.lastProgress = now
		return false
	}
	w.lastObserver = now
	return now.Sub(w.lastProgress) > w.hangWindow
}
