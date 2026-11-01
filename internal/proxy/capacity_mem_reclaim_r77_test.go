//go:build linux

package proxy

import (
	"github.com/khanhkit/pxgo/internal/config"
	"testing"
	"time"
)

func TestR77ZeroMemoryHeadroomCannotBeReusedAfterRelease(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	g.mu.Lock()
	g.active = 40
	g.mu.Unlock()
	sample := capacitySample{available: true, freeFD: 9000, totalFD: 10000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 0, linuxMemoryPressure: true}
	now := time.Unix(60000, 0)
	g.sample(sample, now)
	g.sample(sample, now.Add(4*time.Second))
	g.release()
	if snap := g.snapshot(); snap.EffectiveLimit != 0 {
		t.Fatalf("R77_ZERO_MEMORY_SLOT_REUSED_AFTER_RELEASE: got %+v, no fresh memory headroom", snap)
	}
	healthy := capacitySample{available: true, freeFD: 9000, totalFD: 10000, linuxMemoryConstrained: true, linuxMemoryFreeSlots: 100}
	g.sample(healthy, now.Add(6*time.Second))
	if snap := g.snapshot(); snap.EffectiveLimit == 0 || snap.ActiveDownstream != 39 {
		t.Fatalf("fresh headroom failed to reopen intake without evicting: %+v", snap)
	}
	if err := g.acquire(make(chan struct{}), time.Second); err != nil {
		t.Fatalf("positive fresh cgroup reading did not reopen admission: %v", err)
	}
	g.release()
}
