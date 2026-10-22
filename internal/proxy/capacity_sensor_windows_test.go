//go:build windows

package proxy

import (
	"errors"
	"testing"
)

func TestCapacityWindowsNativeSensorSmoke(t *testing.T) {
	s := readCapacitySample()
	if !s.available || s.resource != capacityWindowsSensor || s.freeSlots <= 0 || s.freeSlots > 4096 {
		t.Fatalf("native Windows memory/handle sample unavailable or invalid: %+v", s)
	}
	t.Logf("native Windows preview: free slots=%d pressure=%v reason=%s", s.freeSlots, s.resourcePressure, s.reason)
}

func TestCapacityWindowsNativeInjectionFailsClosed(t *testing.T) {
	good := capacityWindowsMemory{totalPhys: 16 << 30, availPhys: 12 << 30, totalPageFile: 24 << 30, availPageFile: 14 << 30, load: 20}
	cases := []struct {
		name      string
		handles   uint32
		handleErr error
		mem       capacityWindowsMemory
		memErr    error
		available bool
		minSlots  int
		maxSlots  int
	}{
		{name: "healthy", handles: 200, mem: good, available: true, minSlots: 2560, maxSlots: 4096},
		{name: "native-handles-failure", handleErr: errors.New("GetProcessHandleCount denied"), mem: good},
		{name: "native-memory-failure", handles: 200, memErr: errors.New("GlobalMemoryStatusEx denied")},
		{name: "zero-handles", handles: 0, mem: good},
		{name: "zero-total-memory", handles: 200, mem: capacityWindowsMemory{}},
		{name: "contradictory-memory", handles: 200, mem: capacityWindowsMemory{totalPhys: 512, availPhys: 1024, totalPageFile: 4096, availPageFile: 2048}},
		{name: "host-pressure", handles: 200, mem: capacityWindowsMemory{totalPhys: 4 << 30, availPhys: 256 << 20, totalPageFile: 4 << 30, availPageFile: 256 << 20}, available: true, maxSlots: 0},
		{name: "process-handle-ceiling", handles: 32768, mem: good, available: true, maxSlots: 0},
		{name: "invalid-load", handles: 200, mem: capacityWindowsMemory{totalPhys: 16 << 30, totalPageFile: 24 << 30, load: 101}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readCapacitySampleWindows(func() (uint32, error) { return tc.handles, tc.handleErr }, func() (capacityWindowsMemory, error) { return tc.mem, tc.memErr })
			if got.available != tc.available {
				t.Fatalf("available=%v expected=%v sample=%+v", got.available, tc.available, got)
			}
			if got.freeSlots < tc.minSlots || got.freeSlots > tc.maxSlots {
				t.Fatalf("freeSlots=%d outside [%d,%d]: %+v", got.freeSlots, tc.minSlots, tc.maxSlots, got)
			}
			if !got.available && got.freeSlots != 0 {
				t.Fatal("failed native input granted new capacity")
			}
		})
	}
}
