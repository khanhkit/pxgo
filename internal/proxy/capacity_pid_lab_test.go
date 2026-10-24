package proxy

import (
	"syscall"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestCapacityResearchPoliciesPreserveHardSafety(t *testing.T) {
	for _, policy := range []string{"r11", "pi", "pid", "proportional", "hpa", "fast_saturation"} {
		t.Run(policy, func(t *testing.T) {
			t.Setenv("PXGO_RESEARCH_CAPACITY_POLICY", policy)
			g := newCapacityGovernor(modeAuto, config.Default())
			ts := time.Unix(30000, 0)
			good := capacitySample{available: true, totalFD: 16384, freeFD: 14000}
			for tick := 0; tick < 24; tick++ {
				g.mu.Lock()
				g.active = g.effective
				g.waiting = 1
				g.mu.Unlock()
				g.sample(good, ts.Add(time.Duration(tick)*250*time.Millisecond))
				snap := g.snapshot()
				if snap.EffectiveLimit > snap.HardEnvelope {
					t.Fatalf("admission bypassed FD headroom: %+v", snap)
				}
				if snap.ActiveDownstream > snap.EffectiveLimit && snap.State == capacityStateNormal {
					t.Fatalf("overadmitted without resource pressure: %+v", snap)
				}
			}
			old := g.snapshot()
			g.sample(capacitySample{reason: "sensor unavailable"}, ts.Add(7*time.Second))
			noSensor := g.snapshot()
			if noSensor.SoftWindow != old.SoftWindow || noSensor.EffectiveLimit > old.EffectiveLimit {
				t.Fatalf("sensor failure permitted growth: %+v -> %+v", old, noSensor)
			}
			// 2 consecutive valid hard-pressure samples halve only FUTURE admissions.
			pressure := capacitySample{available: true, totalFD: 16384, freeFD: 120}
			g.sample(pressure, ts.Add(8*time.Second))
			g.sample(pressure, ts.Add(9*time.Second))
			snap := g.snapshot()
			if snap.HardEnvelope > snap.ActiveDownstream {
				t.Fatalf("ignored FD cliff: %+v", snap)
			}
			g.recordAcceptError(syscall.EMFILE)
			if final := g.snapshot(); final.EffectiveLimit != 0 {
				t.Fatalf("EMFILE allowed new admission: %+v", final)
			}
		})
	}
}
