//go:build linux

package proxy

import (
	"errors"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Once a finite cgroup memory controller has been observed, an apparent
// transition to unconstrained memory without trustworthy provenance must
// not silently downgrade admission to FD-only. A genuine no-controller cold
// start may still use FD-only; this fixture covers the mid-process transition.
func TestR95PreviouslyFiniteCgroupDisappearsFailClosed(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	finite := capacitySample{available: true, freeFD: 9000, totalFD: 10000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 3}
	now := time.Unix(510000, 0)
	g.sample(finite, now)
	done := make(chan struct{})
	if err := g.acquire(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if s := g.snapshot(); s.ActiveDownstream != 1 {
		t.Fatalf("initial permit lost: %+v", s)
	}
	// The controller previously reported finite memory, then a valid FD sample
	// arrives with no finite cgroup context (e.g. mount namespace changed).
	g.sample(capacitySample{available: true, freeFD: 9000, totalFD: 10000}, now.Add(time.Second))
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.ActiveDownstream != 1 {
		t.Fatalf("R95_UNVERIFIED_DOWNGRADE_TO_FD_ONLY: %+v", s)
	}
	g.release()
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.ActiveDownstream != 0 {
		t.Fatalf("old connection close reopened unverified intake: %+v", s)
	}
	if err := g.acquire(done, 20*time.Millisecond); err == nil {
		g.release()
		t.Fatal("unverified cgroup removal admitted a new physical socket")
	} else {
		var timeout capacityWaitTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("expected bounded denial, got %v", err)
		}
	}
	finite.linuxMemoryFreeSlots = 2
	g.sample(finite, now.Add(3*time.Second))
	if err := g.acquire(done, 150*time.Millisecond); err != nil {
		t.Fatalf("reestablished finite budget did not reopen: %v", err)
	}
	g.release()
}

func TestR95UnknownTelemetryResourceAfterFiniteCgroupMustFailClosed(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(514000, 0)
	finite := capacitySample{available: true, freeFD: 9000, totalFD: 10000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 3}
	g.sample(finite, now)
	g.sample(capacitySample{available: true, resource: "future-resource-driver",
		freeFD: 9000, totalFD: 10000}, now.Add(time.Second))
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.SensorAvailability != capacityInvalidSensor {
		t.Fatalf("R95_UNKNOWN_TELEMETRY_DRIVER_REUSED_MEMORY_ENVELOPE: %+v", s)
	}
	g.sample(finite, now.Add(2*time.Second))
	if s := g.snapshot(); s.EffectiveLimit == 0 {
		t.Fatalf("valid finite sensor did not reopen: %+v", s)
	}
}

func TestR95NeverFiniteCgroupRetainsOriginalFDOnlyCompatibility(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	g.sample(capacitySample{available: true, freeFD: 9000, totalFD: 10000}, time.Unix(511000, 0))
	if err := g.acquire(make(chan struct{}), 50*time.Millisecond); err != nil {
		t.Fatalf("genuine controller-less initial FD budget blocked: %v", err)
	}
	g.release()
}
