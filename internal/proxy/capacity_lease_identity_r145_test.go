//go:build linux

package proxy

import (
	"github.com/khanhkit/pxgo/internal/config"
	"testing"
	"time"
)

// Red test: a pre-existing connection was admitted when the cgroup was
// unconstrained; after the controller becomes finite, a NEW lease reserves
// one memory slot. Releasing the OLD lease must not settle the NEW lease's
// reservation. Aggregate 'active' doesn't provide enough identity to know.
func TestR145OlderUntrackedReleaseCannotSettleNewerTrackedLiveLease(t *testing.T) {
	cfg := config.Default()
	g := newCapacityGovernor(modeAuto, cfg)
	defer g.close()
	done := make(chan struct{})
	now := time.Now()
	unconstrained := capacitySample{available: true, freeFD: 9000, totalFD: 10000, linuxMemoryConstrained: false}
	g.sample(unconstrained, now)
	if err := g.acquire(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	} // old, no memcg reservation
	finite := capacitySample{available: true, freeFD: 9000, totalFD: 10000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 10}
	g.sample(finite, now.Add(time.Second))
	if err := g.acquire(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	} // new, pending1
	g.mu.Lock()
	if g.active != 2 || g.memoryActiveDebt != 1 {
		t.Fatalf("setup active=%d new-debt=%d", g.active, g.memoryActiveDebt)
	}
	g.mu.Unlock()
	g.release()                     // closes OLD untracked socket, NEW remains active
	finite.linuxMemoryFreeSlots = 9 // unrelated anonymous allocator
	g.sample(finite, now.Add(2*time.Second))
	finite.linuxMemoryFreeSlots = 10 // unrelated allocator exits; new tracked socket still live
	g.sample(finite, now.Add(3*time.Second))
	g.mu.Lock()
	pending := g.memoryActiveDebt
	retired := g.memoryClosedDebt
	debt := g.memoryPendingSlots
	active := g.active
	g.mu.Unlock()
	if pending != 1 || retired != 0 || debt != 1 {
		t.Fatalf("R145_LEASE_IDENTITY_FALSE_SETTLEMENT: old release must not retire NEW live lease debt; active=%d activePending=%d retiredPending=%d totalDebt=%d", active, pending, retired, debt)
	}
	g.release()
}
