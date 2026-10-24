package proxy

import (
	"net/http"
	"testing"
	"time"
)

// Native Windows benchmark measures classification operations and allocations
// independently of real 2560-CONNECT network throughput (separate CI gate).
func BenchmarkConnectionClassOpenPromoteClose(b *testing.B) {
	m := newConnectionClassManager()
	m.sampleBudgets(2560)
	req := &http.Request{Method: http.MethodConnect, Header: make(http.Header)}
	now := time.Unix(1000, 0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		lease := m.open(now)
		lease.classifyRequest(req, now)
		lease.finish()
	}
	if got := m.snapshot(); got.Total != 0 {
		b.Fatalf("leaked permits: %+v", got)
	}
}

func BenchmarkConnectionClassShortToLong(b *testing.B) {
	m := newConnectionClassManager()
	m.sampleBudgets(2560)
	req := &http.Request{Method: http.MethodGet, Header: make(http.Header)}
	now := time.Unix(1000, 0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		lease := m.open(now)
		lease.classifyRequest(req, now)
		lease.classifyRequest(req, now.Add(classDurationThreshold+time.Millisecond))
		lease.finish()
	}
	if got := m.snapshot(); got.Total != 0 {
		b.Fatalf("leaked permits: %+v", got)
	}
}
