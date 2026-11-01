package proxy

import (
	"github.com/khanhkit/pxgo/internal/config"
	"testing"
	"time"
)

func TestR106ExpiredSourceReportsStaleNotFreshDiagnostic(t *testing.T) {
	for _, s := range []capacitySample{
		{available: true, totalFD: 10000, freeFD: 9000},
		{available: true, resource: capacityWindowsSensor, freeSlots: 128},
	} {
		g := newCapacityGovernor(modeAuto, config.Default())
		g.sample(s, time.Unix(720000, 0))
		g.mu.Lock()
		g.lastVerifiedReceipt = time.Now().Add(-2 * capacitySensorMaxAge)
		g.mu.Unlock()
		snap := g.snapshot()
		if snap.SensorAvailability != "stale" || snap.EffectiveLimit != 0 {
			t.Errorf("R106_EXPIRED_SENSOR_DIAGNOSTIC_MISLEADING: %+v", snap)
		}
		g.close()
	}
}
