package diagnostic

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultEventCapacity = 512
	maxEventKindBytes    = 64
	maxEventMessageBytes = 1024
)

type Event struct {
	Sequence uint64    `json:"sequence"`
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Reason   string    `json:"reason"`
}

type Ring struct {
	mu       sync.RWMutex
	capacity int
	next     uint64
	events   []Event
	now      func() time.Time
}

func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultEventCapacity
	}
	return &Ring{
		capacity: capacity,
		events:   make([]Event, 0, capacity),
		now:      time.Now,
	}
}

func (r *Ring) Record(kind, message string) {
	if r == nil {
		return
	}
	kind = boundText(strings.TrimSpace(kind), maxEventKindBytes)
	message = boundText(RedactText(message), maxEventMessageBytes)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	event := Event{
		Sequence: r.next,
		Time:     r.now().UTC(),
		Kind:     kind,
		Reason:   message,
	}
	if len(r.events) == r.capacity {
		copy(r.events, r.events[1:])
		r.events[len(r.events)-1] = event
		return
	}
	r.events = append(r.events, event)
}

func (r *Ring) Snapshot() []Event {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Event(nil), r.events...)
}

func boundText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

var defaultRing atomic.Pointer[Ring]

func init() {
	defaultRing.Store(NewRing(DefaultEventCapacity))
}

func Record(kind, message string) {
	defer func() { _ = recover() }()
	if ring := defaultRing.Load(); ring != nil {
		ring.Record(kind, message)
	}
}

func Events() []Event {
	if ring := defaultRing.Load(); ring != nil {
		return ring.Snapshot()
	}
	return nil
}

func ResetForTest() {
	defaultRing.Store(NewRing(DefaultEventCapacity))
}
