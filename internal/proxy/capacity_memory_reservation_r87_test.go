//go:build linux

package proxy

import (
	"errors"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Positive cgroup headroom that has NOT changed after a new admission is
// not a fresh grant of an additional 1MiB/socket virtual reservation.
func TestR87RepeatedIdenticalMemoryTelemetryDoesNotMintNewPermits(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(220000, 0)
	one := capacitySample{available: true, freeFD: 9000, totalFD: 10000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 1}
	g.sample(one, now)
	done := make(chan struct{})
	if err := g.acquire(done, 50*time.Millisecond); err != nil {
		t.Fatalf("one verified slot denied: %v", err)
	}
	g.release()
	for i := 0; i < 3; i++ {
		// Kernel telemetry still reports exactly the same free-slot estimate,
		// even though a socket was just admitted and closed.
		g.sample(one, now.Add(time.Duration(i+1)*time.Second))
		err := g.acquire(done, 10*time.Millisecond)
		if err == nil {
			g.release()
			t.Fatalf("REPEATED_IDENTICAL_CGROUP_SAMPLE_CREATED_PERMIT #%d", i+1)
		}
		var timeout capacityWaitTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("blocked admission expected timeout: %v", err)
		}
	}
	// An increased free-slot measurement proves *some* new headroom exists.
	two := one
	two.linuxMemoryFreeSlots = 2
	g.sample(two, now.Add(5*time.Second))
	if err := g.acquire(done, 100*time.Millisecond); err != nil {
		t.Fatalf("newly observed increased memory budget failed to reopen: %v", err)
	}
	g.release()
}

// Kernel accounting may eventually reflect the bytes of a previously
// admitted socket. A matching decrease should retire pending reservation
// debt; a later increase should then legitimately reopen the measured budget.
func TestR87RecordedMemoryConsumptionAndRecovery(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(223000, 0)
	s := capacitySample{available: true, freeFD: 9000, totalFD: 10000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 2}
	g.sample(s, now)
	done := make(chan struct{})
	for i := 0; i < 2; i++ {
		if err := g.acquire(done, 100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		g.release()
	}
	s.linuxMemoryFreeSlots = 0
	g.sample(s, now.Add(time.Second))
	s.linuxMemoryFreeSlots = 2
	g.sample(s, now.Add(2*time.Second))
	for i := 0; i < 2; i++ {
		if err := g.acquire(done, 100*time.Millisecond); err != nil {
			t.Fatalf("observed consumption/recovery slot %d denied: %v", i, err)
		}
		g.release()
	}
}
