//go:build linux

package proxy

import (
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestR76CgroupV2MemoryParse(t *testing.T) {
	tests := []struct {
		name, limit, current, high, psi string
		constrained                     bool
		slots                           int
		pressure                        bool
		bad                             bool
	}{
		{"two_gib", "2147483648", "536870912", "max", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", true, 1280, false, false},
		{"near_oom", "536870912", "503316480", "max", "", true, 0, true, false},
		{"current_exceeds_limit", "536870912", "600000000", "max", "", true, 0, true, false},
		{"unlimited", "max", "1234", "max", "", false, 0, false, false},
		{"unlimited_with_psi", "max", "1234", "max", "some avg10=30.0 avg60=1.0 avg300=0.0 total=100", false, 0, true, false},
		{"high_throttle", "2147483648", "1073741824", "805306368", "", true, 0, true, false},
		{"unlimited_max_but_finite_high", "max", "536870912", "1073741824", "", true, 384, false, false},
		{"psi_some", "2147483648", "536870912", "max", "some avg10=11.00 avg60=2.00 avg300=0.00 total=100\n", true, 1280, true, false},
		{"psi_full", "2147483648", "536870912", "max", "full avg10=1.50 avg60=0.00 avg300=0.00 total=100\n", true, 1280, true, false},
		{"bad_current", "536870912", "INVALID", "max", "", false, 0, false, true},
		{"bad_limit", "-1", "0", "max", "", false, 0, false, true},
		{"bad_high", "536870912", "0", "WRONG", "", false, 0, false, true},
		{"zero_limit", "0", "0", "max", "", false, 0, false, true},
		{"overflow", "18446744073709551616", "0", "max", "", false, 0, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCapacityCgroupMemory([]byte(tc.limit), []byte(tc.current), []byte(tc.high), []byte(tc.psi))
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v bad=%t", err, tc.bad)
			}
			if tc.bad {
				return
			}
			if got.constrained != tc.constrained || got.freeSlots != tc.slots || got.pressured != tc.pressure {
				t.Fatalf("got %+v; want constrained=%t slots=%d pressured=%t", got, tc.constrained, tc.slots, tc.pressure)
			}
		})
	}
}

func TestR76MemoryEnvelopeCapsAdmissionWithoutEvictingLive(t *testing.T) {
	g := r67OneOccupiedGovernor()
	defer g.close()
	g.mu.Lock()
	g.soft = 256
	g.hard = 256
	g.active = 40
	g.recomputeLocked()
	g.mu.Unlock()
	pressured := capacitySample{available: true, freeFD: 9000, totalFD: 10000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 0, linuxMemoryPressure: true}
	now := time.Unix(50000, 0)
	g.sample(pressured, now)
	g.sample(pressured, now.Add(4*time.Second))
	snap := g.snapshot()
	if snap.HardEnvelope != 40 || snap.ActiveDownstream != 40 || snap.EffectiveLimit > 40 || snap.State != capacityStatePressure || snap.PressureReason != "cgroup-memory" {
		t.Fatalf("memory pressure must retain 40 held permits and block new admissions: %+v", snap)
	}
}

func TestR76CgroupRecoveryNeedsConsecutiveFreshSamples(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	g.mu.Lock()
	g.active = 24
	g.mu.Unlock()
	now := time.Unix(55000, 0)
	pressured := capacitySample{
		available: true, freeFD: 9000, totalFD: 10000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 0, linuxMemoryPressure: true,
	}
	g.sample(pressured, now)
	g.sample(pressured, now.Add(4*time.Second))
	if s := g.snapshot(); s.State != capacityStatePressure || s.HardEnvelope != 24 {
		t.Fatalf("pressure failed to contract: %+v", s)
	}
	healthy := capacitySample{
		available: true, freeFD: 9000, totalFD: 10000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 300,
	}
	for i := 0; i < 6; i++ {
		g.sample(healthy, now.Add(time.Duration(8+i)*time.Second))
	}
	snap := g.snapshot()
	if snap.State != capacityStateNormal || snap.ActiveDownstream != 24 || snap.HardEnvelope != 324 {
		t.Fatalf("recovery must preserve active, restore headroom and exit pressure: %+v", snap)
	}
	if err := g.acquire(make(chan struct{}), time.Second); err != nil {
		t.Fatalf("recovered limit failed admission: %v", err)
	}
	g.release()
}
