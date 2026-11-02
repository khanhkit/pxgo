//go:build linux

package proxy

import (
	"github.com/khanhkit/pxgo/internal/config"
	"math/rand"
	"testing"
	"time"
)

// Bounded property model for exact-id permit lifecycle and conservative
// reservation-count consistency. Synthetic free slots can go up/down due to
// external processes; no negative aggregate delta is an allocation receipt.
func TestR145PropertyExactLeaseLedgerAndIdempotentRelease(t *testing.T) {
	for _, seed := range []int64{7, 101, 251, 3457} {
		r := rand.New(rand.NewSource(seed))
		g := newCapacityGovernor(modeAuto, config.Default())
		now := time.Now()
		s := capacitySample{available: true, freeFD: 10000, totalFD: 20000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 16}
		g.sample(s, now)
		ids := make([]uint64, 0, 16)
		done := make(chan struct{})
		for i := 0; i < 800; i++ {
			switch r.Intn(5) {
			case 0, 1:
				s.linuxMemoryFreeSlots = max(0, min(32, s.linuxMemoryFreeSlots+r.Intn(7)-3))
				g.sample(s, now.Add(time.Duration(i+1)*time.Second))
			case 2, 3:
				if id, err := g.acquireLease(done, 250*time.Microsecond); err == nil {
					ids = append(ids, id)
				}
			case 4:
				if len(ids) > 0 {
					n := r.Intn(len(ids))
					id := ids[n]
					g.releaseLease(id)
					g.releaseLease(id)
					ids = append(ids[:n], ids[n+1:]...)
				}
			}
			g.mu.Lock()
			active, tracked, retired, pending, remaining := g.active, g.memoryActiveDebt, g.memoryClosedDebt, g.memoryPendingSlots, g.memorySampleRemaining
			counted := 0
			for _, reserved := range g.leaseReservations {
				if reserved {
					counted++
				}
			}
			entries := len(g.leaseReservations)
			g.mu.Unlock()
			if entries != len(ids) || active != len(ids) || tracked != counted || tracked+retired != pending || tracked < 0 || retired < 0 || remaining < 0 {
				t.Fatalf("seed=%d round=%d inconsistent: entries=%d ids=%d active=%d tracked=%d counted=%d retired=%d pending=%d remaining=%d", seed, i, entries, len(ids), active, tracked, counted, retired, pending, remaining)
			}
		}
		for _, id := range ids {
			g.releaseLease(id)
		}
		g.mu.Lock()
		active, entries := g.active, len(g.leaseReservations)
		g.mu.Unlock()
		g.close()
		if active != 0 || entries != 0 {
			t.Fatalf("seed=%d leaked active=%d entries=%d", seed, active, entries)
		}
	}
}
