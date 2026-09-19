package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTCSUPCONC019ConcurrentFailureStormIsBounded(t *testing.T) {
	block := make(chan struct{})
	var idleCalls atomic.Int32
	s := New(Owners{
		CloseIdleTransports: func(ctx context.Context) error {
			idleCalls.Add(1)
			select {
			case <-block:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	defer s.Close()

	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := ProxyKey("proxy-a")
			if i%2 == 1 {
				key = "proxy-b"
			}
			s.Recover(Outcome{Kind: OutcomeProxyDialFailure, Proxy: key})
		}(i)
	}
	wg.Wait()

	status := s.Status()
	if status.HealthEntries > maxHealthEntries {
		t.Fatalf("health entries = %d, max %d", status.HealthEntries, maxHealthEntries)
	}
	if status.InflightActions > 4 {
		t.Fatalf("inflight actions = %d, max named owner actions 4", status.InflightActions)
	}
	if idleCalls.Load() > 1 {
		t.Fatalf("idle cleanup calls while blocked = %d, want <=1", idleCalls.Load())
	}
	close(block)
	waitUntil(t, func() bool { return s.Status().InflightActions == 0 })
}

func BenchmarkSupervisorRecord(b *testing.B) {
	s := New(Owners{})
	defer s.Close()
	outcome := Outcome{Kind: OutcomeDestinationFailure}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Record(outcome)
	}
}

func BenchmarkSupervisorOrderFourCandidates(b *testing.B) {
	s := New(Owners{})
	defer s.Close()
	authoritative := []Candidate{
		{Key: "proxy-a"},
		{Key: "proxy-b"},
		{Key: "proxy-c"},
		{Key: "direct://DIRECT:80", Direct: true},
	}
	s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Order(authoritative)
	}
}
