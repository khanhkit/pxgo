//go:build linux

package proxy

import "testing"

// Fuzz with untrusted kernel telemetry payloads. A parser must fail safely:
// no panic, no negative capacity, and never an unconstrained finite limit.
func FuzzR76CgroupMemoryParser(f *testing.F) {
	f.Add("2147483648", "536870912", "max", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0")
	f.Add("max", "0", "max", "")
	f.Add("0", "not-int", "bad", "some avg10=nan")
	f.Fuzz(func(t *testing.T, limit, current, high, psi string) {
		if len(limit) > 80 || len(current) > 80 || len(high) > 80 || len(psi) > 1024 {
			return
		}
		parsed, err := parseCapacityCgroupMemory([]byte(limit), []byte(current), []byte(high), []byte(psi))
		if err == nil && parsed.freeSlots < 0 {
			t.Fatalf("negative slots: %+v", parsed)
		}
		if err == nil && !parsed.constrained && parsed.freeSlots != 0 {
			t.Fatalf("unconstrained state created capacity: %+v", parsed)
		}
	})
}
