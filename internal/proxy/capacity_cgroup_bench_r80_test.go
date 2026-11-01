//go:build linux

package proxy

import "testing"

func BenchmarkR80CgroupHierarchyHostSample(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s := readCapacitySample()
		if !s.available || s.linuxMemoryFreeSlots < 0 {
			b.Fatalf("invalid sample: %+v", s)
		}
	}
}

func BenchmarkR80CgroupHierarchyNativeRoot(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := readCapacityCgroupMemory("/sys/fs/cgroup"); err != nil {
			b.Fatal(err)
		}
	}
}
