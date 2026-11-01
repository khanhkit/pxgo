//go:build linux

package proxy

import "testing"

func BenchmarkR76CgroupParse(b *testing.B) {
	limit := []byte("2147483648\n")
	current := []byte("536870912\n")
	high := []byte("max\n")
	psi := []byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parseCapacityCgroupMemory(limit, current, high, psi); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkR76HostCapacitySample(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if s := readCapacitySample(); !s.available {
			b.Fatalf("sample failed: %s", s.reason)
		}
	}
}
