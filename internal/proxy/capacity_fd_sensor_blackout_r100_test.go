//go:build linux

package proxy

import (
	"errors"
	"github.com/khanhkit/pxgo/internal/config"
	"testing"
	"time"
)

// A trustworthy FD-only sample may be safe at first startup on a host without
// cgroup v2. But a later missing FD sample cannot keep authorizing new TCP
// sockets from a stale FD budget. Existing physical connections must survive.
func TestR100FDOnlyAfterHealthyOutageFencesNewPhysicalAdmission(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(510000, 0)
	healthy := capacitySample{available: true, totalFD: 10000, freeFD: 9000}
	g.sample(healthy, now)
	done := make(chan struct{})
	if e := g.acquire(done, 100*time.Millisecond); e != nil {
		t.Fatal(e)
	}
	before := g.snapshot()
	if before.ActiveDownstream != 1 || before.EffectiveLimit == 0 {
		t.Fatalf("bad fixture %+v", before)
	}
	g.sample(capacitySample{reason: "proc-self-fd-unreadable"}, now.Add(time.Second))
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.ActiveDownstream != 1 {
		t.Fatalf("FD_ONLY_STALE_HARD_ENVELOPE_OPEN: %+v", s)
	}
	if e := g.acquire(done, 20*time.Millisecond); e == nil {
		g.release()
		t.Fatal("FD only sensor blackout allowed unverified new socket")
	} else {
		var timeout capacityWaitTimeout
		if !errors.As(e, &timeout) {
			t.Fatalf("expected bounded timeout, got %v", e)
		}
	}
	g.release()
	if s := g.snapshot(); s.ActiveDownstream != 0 || s.EffectiveLimit != 0 {
		t.Fatalf("old connection close minted a permit %+v", s)
	}
	g.sample(healthy, now.Add(3*time.Second))
	if e := g.acquire(done, 100*time.Millisecond); e != nil {
		t.Fatalf("healthy FD-only recovery blocked: %v", e)
	}
	g.release()
}

func TestR100FDOnlyAfterHealthyContradictoryFDRevokesNewAdmission(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(511000, 0)
	g.sample(capacitySample{available: true, totalFD: 10000, freeFD: 9000}, now)
	g.sample(capacitySample{available: true, totalFD: 100, freeFD: 101}, now.Add(time.Second))
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.SensorAvailability != capacityInvalidSensor {
		t.Fatalf("FD_ONLY_CONTRADICTORY_SAMPLE_OPEN: %+v", s)
	}
}
