//go:build linux

package proxy

import (
	"math/rand"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Source-level determinism only: repeatedly alternate valid cgroup telemetry,
// unavailable and contradictory FD samples, admission and close. No Linux
// kernel controller is modified and no OOM is induced.
func TestR91SensorOutageAdmissionLedgerModel(t *testing.T) {
	const sequences = 50000
	const steps = 96
	for seed := int64(0); seed < sequences; seed++ {
		r := rand.New(rand.NewSource(seed))
		g := newCapacityGovernor(modeAuto, config.Default())
		now := time.Unix(400000, 0)
		done := make(chan struct{})
		observedFinite := false
		for step := 0; step < steps; step++ {
			tick := now.Add(time.Duration(step) * time.Second)
			switch r.Intn(7) {
			case 0, 1, 2:
				s := capacitySample{available: true, totalFD: 21000, freeFD: 20000,
					linuxMemoryConstrained: true, linuxMemoryFreeSlots: r.Intn(11)}
				before := g.snapshot().ActiveDownstream
				g.sample(s, tick)
				observedFinite = true
				if actual := g.snapshot().ActiveDownstream; actual != before {
					t.Fatalf("seed%d step%d sample evicted/created held transport: %d->%d", seed, step, before, actual)
				}
			case 3:
				g.sample(capacitySample{reason: "fd-usage-unavailable"}, tick)
				if observedFinite {
					if s := g.snapshot(); s.EffectiveLimit != 0 {
						t.Fatalf("seed%d step%d LOST_SENSOR_OPEN_GATE: %+v", seed, step, s)
					}
				}
			case 4:
				g.sample(capacitySample{available: true, freeFD: 101, totalFD: 100}, tick)
				if observedFinite {
					if s := g.snapshot(); s.EffectiveLimit != 0 {
						t.Fatalf("seed%d step%d CONTRADICTORY_SENSOR_OPEN_GATE: %+v", seed, step, s)
					}
				}
			case 5:
				snap := g.snapshot()
				if snap.ActiveDownstream < snap.EffectiveLimit {
					if err := g.acquire(done, 40*time.Millisecond); err != nil {
						t.Fatalf("seed%d step%d expected physical permit: %v (snap=%+v)", seed, step, err, snap)
					}
				}
			case 6:
				g.release()
			}
			g.mu.Lock()
			if g.active < 0 || g.memoryPendingSlots < 0 || g.memorySampleRemaining < 0 {
				t.Fatalf("negative counters seed=%d step=%d active=%d pending=%d remaining=%d",
					seed, step, g.active, g.memoryPendingSlots, g.memorySampleRemaining)
			}
			if g.memoryAdmissionBlocked && g.effective != 0 {
				t.Fatalf("memory safety gate lost seed=%d step=%d effective=%d", seed, step, g.effective)
			}
			if g.memorySampleConstrained && g.memorySampleRemaining > g.memoryLastObservedSlots {
				t.Fatalf("duplicate cgroup headroom seed=%d step=%d remaining=%d observed=%d", seed, step, g.memorySampleRemaining, g.memoryLastObservedSlots)
			}
			g.mu.Unlock()
		}
		g.close()
	}
}
