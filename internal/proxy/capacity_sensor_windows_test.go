//go:build windows

package proxy

import "testing"

func TestCapacityWindowsNativeSensorSmoke(t *testing.T) {
	s := readCapacitySample()
	if !s.available || s.resource != capacityWindowsSensor || s.freeSlots <= 0 || s.freeSlots > 4096 {
		t.Fatalf("native Windows memory/handle sample unavailable or invalid: %+v", s)
	}
	t.Logf("native Windows preview: free slots=%d pressure=%v reason=%s", s.freeSlots, s.resourcePressure, s.reason)
}
