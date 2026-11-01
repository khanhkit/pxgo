package proxy

import (
	"errors"
	"github.com/khanhkit/pxgo/internal/config"
	"testing"
	"time"
)

// Negative-before-positive real monotonic-clock experiment. Unlike tests
// using injected synthetic sample timestamps, no sensor update occurs after
// the valid initial measurement.
func TestR105StalledCapacitySamplerCannotKeepAcceptingFromStaleEnvelope(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	sample := capacitySample{available: true, totalFD: 20000, freeFD: 19000}
	g.sample(sample, time.Unix(600000, 0))
	if s := g.snapshot(); s.EffectiveLimit == 0 {
		t.Fatalf("invalid initial headroom %+v", s)
	}
	time.Sleep(5200 * time.Millisecond) // candidate stale bound is 5s
	if err := g.acquire(make(chan struct{}), 35*time.Millisecond); err == nil {
		g.release()
		t.Fatal("R105_STALE_SENSOR_ADMITTED_PHYSICAL_SOCKET_5S_AFTER_LAST_SAMPLE")
	} else {
		var timeout capacityWaitTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("expected bounded deny during missing sensor ticks: %v", err)
		}
	}
	// An actual new validated reading restores the same unchanged OS budget.
	g.sample(sample, time.Unix(600005, 0))
	if err := g.acquire(make(chan struct{}), 150*time.Millisecond); err != nil {
		t.Fatalf("fresh sample did not reopen intake: %v", err)
	}
	g.release()
}

func TestR105FiniteCgroupAndWindowsSensorExpiryAreBothBounded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sample capacitySample
	}{
		{"finite-linux-cgroup", capacitySample{available: true, totalFD: 10000, freeFD: 9000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 24}},
		{"native-windows-model", capacitySample{available: true, resource: capacityWindowsSensor, freeSlots: 150}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newCapacityGovernor(modeAuto, config.Default())
			defer g.close()
			g.sample(tc.sample, time.Unix(701000, 0))
			g.mu.Lock()
			g.lastVerifiedReceipt = time.Now().Add(-capacitySensorMaxAge - time.Second)
			g.mu.Unlock()
			if s := g.snapshot(); s.EffectiveLimit != 0 || s.ActiveDownstream != 0 {
				t.Fatalf("expired OS memory/handles telemetry left physical intake open: %+v", s)
			}
			if e := g.acquire(make(chan struct{}), 15*time.Millisecond); e == nil {
				g.release()
				t.Fatal("stale memory/handles estimate minted permit")
			}
			g.sample(tc.sample, time.Unix(701001, 0))
			if e := g.acquire(make(chan struct{}), 100*time.Millisecond); e != nil {
				t.Fatalf("new valid receipt did not restore intake: %v", e)
			}
			g.release()
		})
	}
}
