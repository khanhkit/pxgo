//go:build linux

package proxy

import (
	"github.com/khanhkit/pxgo/internal/config"
	"sync"
	"testing"
	"time"
)

// Validate exact ID release in adversarial scheduling with double-close.
// The real net.Conn uses releaseOnce, but stale shutdown paths must not
// accidentally release another live physical permit on a duplicate ID.
func TestR145ParallelExactLeaseReleaseNoExtraPermits(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Now()
	sample := capacitySample{available: true, freeFD: 10000, totalFD: 20000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 256}
	g.sample(sample, now)
	ids := make([]uint64, 64)
	done := make(chan struct{})
	for i := range ids {
		id, err := g.acquireLease(done, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("permit %d: %v", i, err)
		}
		ids[i] = id
	}
	if len(g.leaseReservations) != 64 || g.memoryActiveDebt != 64 {
		t.Fatalf("setup map=%d debt=%d", len(g.leaseReservations), g.memoryActiveDebt)
	}
	var wg sync.WaitGroup
	for i := len(ids) - 1; i >= 0; i-- {
		id := ids[i]
		wg.Add(1)
		go func() { defer wg.Done(); g.releaseLease(id); g.releaseLease(id) }()
	}
	wg.Wait()
	g.mu.Lock()
	active, tracked, retired, entries := g.active, g.memoryActiveDebt, g.memoryClosedDebt, len(g.leaseReservations)
	g.mu.Unlock()
	if active != 0 || tracked != 0 || retired != 64 || entries != 0 {
		t.Fatalf("parallel/double release: active=%d tracked=%d retired=%d entries=%d", active, tracked, retired, entries)
	}
}
