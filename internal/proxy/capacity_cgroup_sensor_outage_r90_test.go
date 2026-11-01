//go:build linux

package proxy

import (
	"errors"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Once a process has observed a finite cgroup memory controller, an
// unreadable/contradictory resource measurement must not permit admissions
// from the last known hard envelope. Existing leases must stay alive.
func TestR90CgroupFiniteSensorOutageDeniesNewButKeepsExisting(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(300000, 0)
	ok := capacitySample{available: true, freeFD: 20000, totalFD: 21000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 100}
	g.sample(ok, now)
	done := make(chan struct{})
	if err := g.acquire(done, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	first := g.snapshot()
	if first.ActiveDownstream != 1 || first.EffectiveLimit == 0 {
		t.Fatalf("bad setup: %+v", first)
	}
	g.sample(capacitySample{reason: "rlimit-unavailable"}, now.Add(time.Second))
	state := g.snapshot()
	if state.ActiveDownstream != 1 || state.EffectiveLimit != 0 {
		t.Fatalf("R90_STALE_MEMORY_CEILING_OPEN_AFTER_SENSOR_LOSS: %+v", state)
	}
	if err := g.acquire(done, 20*time.Millisecond); err == nil {
		g.release()
		t.Fatal("new physical admission succeeded without memory telemetry")
	} else {
		var timeout capacityWaitTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("expected bounded admission timeout, got %v", err)
		}
	}
	g.release()
	if s := g.snapshot(); s.ActiveDownstream != 0 || s.EffectiveLimit != 0 {
		t.Fatalf("release fabricated admission headroom: %+v", s)
	}
	g.sample(ok, now.Add(3*time.Second))
	if err := g.acquire(done, 150*time.Millisecond); err != nil {
		t.Fatalf("valid fresh sample failed to restore admission: %v", err)
	}
	g.release()
}

func TestR90CgroupFiniteContradictoryFDMeasureRevokesNewAdmissions(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(302000, 0)
	g.sample(capacitySample{available: true, freeFD: 20000, totalFD: 21000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 100}, now)
	g.sample(capacitySample{available: true, freeFD: 101, totalFD: 100}, now.Add(time.Second))
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.ActiveDownstream != 0 {
		t.Fatalf("R90_CONTRADICTORY_FD_SAMPLE_REUSED_OLD_MEMORY_BUDGET: %+v", s)
	}
}
