//go:build linux

package proxy

import (
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestR145ExactLeaseOutOfOrderCloseAndDuplicateRelease(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Now()
	sample := capacitySample{available: true, freeFD: 9000, totalFD: 10000}
	g.sample(sample, now)
	done := make(chan struct{})
	old, err := g.acquireLease(done, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	sample.linuxMemoryConstrained = true
	sample.linuxMemoryFreeSlots = 10
	g.sample(sample, now.Add(time.Second))
	tracked1, err := g.acquireLease(done, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	tracked2, err := g.acquireLease(done, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if old == tracked1 || tracked1 == tracked2 || old == 0 {
		t.Fatalf("lease ID collision: %d,%d,%d", old, tracked1, tracked2)
	}
	g.releaseLease(old) // releasing untracked OLD must not touch newer finite debts
	if g.memoryActiveDebt != 2 || g.memoryClosedDebt != 0 {
		t.Fatalf("old lease misattributed: activePending=%d retired=%d", g.memoryActiveDebt, g.memoryClosedDebt)
	}
	g.releaseLease(old) // same exact stale ID cannot release any other lease
	if g.active != 2 || g.memoryActiveDebt != 2 {
		t.Fatalf("duplicate released live lease: active=%d pending=%d", g.active, g.memoryActiveDebt)
	}
	g.releaseLease(tracked2) // out-of-order FINITE tracked lease
	if g.active != 1 || g.memoryActiveDebt != 1 || g.memoryClosedDebt != 1 {
		t.Fatalf("out of order: active=%d activeDebt=%d retired=%d", g.active, g.memoryActiveDebt, g.memoryClosedDebt)
	}
	g.releaseLease(tracked1)
	if len(g.leaseReservations) != 0 || g.active != 0 || g.memoryActiveDebt != 0 || g.memoryClosedDebt != 2 {
		t.Fatalf("leak: map=%d active=%d activeDebt=%d retired=%d", len(g.leaseReservations), g.active, g.memoryActiveDebt, g.memoryClosedDebt)
	}
}

func TestR145ExactLeaseMapRemainsBoundedAfterManyShortConnections(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	s := capacitySample{available: true, freeFD: 9000, totalFD: 10000}
	g.sample(s, time.Now())
	done := make(chan struct{})
	for n := 0; n < 2048; n++ {
		id, err := g.acquireLease(done, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("lease %d: %v", n, err)
		}
		g.releaseLease(id)
	}
	if len(g.leaseReservations) != 0 || g.active != 0 {
		t.Fatalf("unbounded/stale map: %d entries, %d active", len(g.leaseReservations), g.active)
	}
}
