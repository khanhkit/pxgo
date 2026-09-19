package guardian

import "time"

var restartBackoffSchedule = [...]time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
}

const stableRunReset = 5 * time.Minute

type Backoff struct {
	level int
}

func (b *Backoff) Next() time.Duration {
	if b.level < 0 {
		b.level = 0
	}
	idx := b.level
	if idx >= len(restartBackoffSchedule) {
		idx = len(restartBackoffSchedule) - 1
	}
	delay := restartBackoffSchedule[idx]
	if b.level < len(restartBackoffSchedule)-1 {
		b.level++
	}
	return delay
}

func (b *Backoff) NextDelay() time.Duration {
	idx := b.level
	if idx < 0 {
		idx = 0
	}
	if idx >= len(restartBackoffSchedule) {
		idx = len(restartBackoffSchedule) - 1
	}
	return restartBackoffSchedule[idx]
}

func (b *Backoff) Reset() {
	b.level = 0
}

func (b *Backoff) Level() int {
	if b.level < 0 {
		return 0
	}
	return b.level
}

func (b *Backoff) ObserveReadyDuration(duration time.Duration) bool {
	if duration < stableRunReset {
		return false
	}
	b.Reset()
	return true
}
