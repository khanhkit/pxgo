package proxy

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// A deliberate availability tradeoff: exactly one EMFILE recovery probe
// remains possible despite stale OS telemetry, bounded by the previously
// established physical hard/soft/manual envelopes.
func TestR106StaleSensorRetainsAtMostOneEmergencyFDProbe(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	g.sample(capacitySample{available: true, totalFD: 10000, freeFD: 9000}, time.Now())
	g.recordAcceptError(syscall.EMFILE)
	g.mu.Lock()
	g.lastVerifiedReceipt = time.Now().Add(-2 * capacitySensorMaxAge)
	late := g.cooldown.Add(time.Millisecond)
	g.mu.Unlock()
	g.sample(capacitySample{reason: "fd sensor unavailable"}, late)
	if s := g.snapshot(); s.EffectiveLimit != 1 || s.ActiveDownstream != 0 {
		t.Fatalf("expected exactly one bounded emergency probe even if stale: %+v", s)
	}
	done := make(chan struct{})
	if e := g.acquire(done, 100*time.Millisecond); e != nil {
		t.Fatalf("one recovery probe did not proceed: %v", e)
	}
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.ActiveDownstream != 1 {
		t.Fatalf("second recovery probe incorrectly available: %+v", s)
	}
	if e := g.acquire(done, 10*time.Millisecond); e == nil {
		g.release()
		t.Fatal("stale sensor authorized a second emergency probe")
	} else {
		var timeout capacityWaitTimeout
		if !errors.As(e, &timeout) {
			t.Fatalf("unexpected error: %v", e)
		}
	}
	g.release()
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.ActiveDownstream != 0 {
		t.Fatalf("emergency permit leaked or reopened: %+v", s)
	}
}

// The normal FD-only stale case is NOT an emergency: even releasing the old
// physical socket cannot authorize another new socket without a new sample.
func TestR106OrdinaryStaleSampleCannotReuseClosedSocketHeadroom(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	s := capacitySample{available: true, totalFD: 10000, freeFD: 9000}
	g.sample(s, time.Now())
	if e := g.acquire(make(chan struct{}), 100*time.Millisecond); e != nil {
		t.Fatal(e)
	}
	g.mu.Lock()
	g.lastVerifiedReceipt = time.Now().Add(-2 * capacitySensorMaxAge)
	g.mu.Unlock()
	g.release()
	if snap := g.snapshot(); snap.ActiveDownstream != 0 || snap.EffectiveLimit != 0 {
		t.Fatalf("old socket release reopened stale headroom: %+v", snap)
	}
	g.sample(s, time.Now())
	if e := g.acquire(make(chan struct{}), 100*time.Millisecond); e != nil {
		t.Fatalf("valid OS sample failed to restore: %v", e)
	}
	g.release()
}
