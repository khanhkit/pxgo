//go:build linux

package proxy

import (
	"errors"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Releasing old established sockets must not manufacture additional permits
// from a positive-but-small *stale* cgroup memory snapshot.
func TestR85SmallFreshMemoryBudgetIsConsumedNotReused(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	g.mu.Lock()
	g.active = 40 // already-held useful long connections
	g.mu.Unlock()
	measured := capacitySample{
		available: true, freeFD: 9000, totalFD: 10000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 1,
	}
	now := time.Unix(200000, 0)
	g.sample(measured, now)
	for i := 0; i < 40; i++ {
		g.release()
	}
	done := make(chan struct{})
	if err := g.acquire(done, 200*time.Millisecond); err != nil {
		t.Fatalf("one fresh resource slot must be admitted: %v", err)
	}
	defer g.release()
	err := g.acquire(done, 20*time.Millisecond)
	if err == nil {
		g.release()
		t.Fatalf("UNMEASURED_POSITIVE_MEMORY_SLOT_REUSED: second new connection admitted from one-slot stale measurement")
	}
	var timeout capacityWaitTimeout
	if !errors.As(err, &timeout) {
		t.Fatalf("expected bounded timeout with consumed memory budget, got %v", err)
	}
	// An INCREASED observed headroom can authorize one more slot;
	// repeated identical telemetry cannot refresh virtual memory permits.
	fresh := measured
	fresh.linuxMemoryFreeSlots = 2
	g.sample(fresh, now.Add(4*time.Second))
	if err := g.acquire(done, 200*time.Millisecond); err != nil {
		t.Fatalf("fresh measured memory slot did not reopen: %v", err)
	}
	g.release()
}

func TestR85MemoryBudgetNeverDoubleCountsOnCloseAndReopen(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	s := capacitySample{
		available: true, freeFD: 9000, totalFD: 10000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 2,
	}
	g.sample(s, time.Unix(210000, 0))
	done := make(chan struct{})
	for i := 0; i < 2; i++ {
		if err := g.acquire(done, 50*time.Millisecond); err != nil {
			t.Fatalf("admission %d: %v", i, err)
		}
		g.release() // close immediately; still spent an *unverified* socket resource
	}
	if err := g.acquire(done, 20*time.Millisecond); err == nil {
		g.release()
		t.Fatal("THIRD_UNMEASURED_SOCKET_AFTER_TWO_ADMISSIONS")
	}
}
