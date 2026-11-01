//go:build linux

package proxy

import (
	"errors"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Server.Start() samples the sensor BEFORE exposing the listener. If that
// first sample fails, the provisional 256-slot AUTO cold-start window must
// not be mistaken for an OS-verified safe physical admission budget.
func TestR93FirstFailedPreflightFencesColdStartAdmission(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(480000, 0)
	if initial := g.snapshot(); initial.EffectiveLimit != 256 {
		t.Fatalf("unpublished cold start envelope changed: %+v", initial)
	}
	g.sample(capacitySample{reason: "RLIMIT_NOFILE unavailable"}, now)
	if s := g.snapshot(); s.ActiveDownstream != 0 || s.EffectiveLimit != 0 {
		t.Fatalf("R93_UNVERIFIED_COLD_START_ADMITTED: %+v", s)
	}
	err := g.acquire(make(chan struct{}), 20*time.Millisecond)
	var timeout capacityWaitTimeout
	if !errors.As(err, &timeout) {
		if err == nil {
			g.release()
		}
		t.Fatalf("unverified AUTO preflight admission error=%v", err)
	}
	valid := capacitySample{available: true, freeFD: 9000, totalFD: 10000}
	g.sample(valid, now.Add(time.Second))
	if s := g.snapshot(); s.EffectiveLimit == 0 || s.SensorAvailability != "fresh" {
		t.Fatalf("valid FD preflight failed to recover: %+v", s)
	}
	if err := g.acquire(make(chan struct{}), 100*time.Millisecond); err != nil {
		t.Fatalf("valid FD sensor did not reopen: %v", err)
	}
	g.release()
}

func TestR93FirstContradictoryFDPreflightCannotCreateColdStartPermit(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Unix(481000, 0)
	g.sample(capacitySample{available: true, freeFD: 200, totalFD: 100}, now)
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.SensorAvailability != capacityInvalidSensor {
		t.Fatalf("R93_INVALID_COLD_START_USED_STALE_256: %+v", s)
	}
	g.sample(capacitySample{available: true, freeFD: 9990, totalFD: 10000}, now.Add(time.Second))
	if s := g.snapshot(); s.EffectiveLimit == 0 {
		t.Fatalf("valid sensor failed to reopen cold-start AUTO: %+v", s)
	}
}
