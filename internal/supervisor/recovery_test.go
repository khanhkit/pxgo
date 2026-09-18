package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitUntil(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached before timeout")
}

func TestTCSUPREC008RecoveryActionSingleflightAndRetry(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	s := testSupervisor(t, clock, Owners{
		RefreshRoute: func(ctx context.Context) error {
			calls.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Trigger(ActionRefreshRoute)
		}()
	}
	wg.Wait()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("owner action did not start")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls while blocked = %d, want 1", got)
	}
	close(release)
	waitUntil(t, func() bool { return s.Status().InflightActions == 0 })
	s.Trigger(ActionRefreshRoute)
	waitUntil(t, func() bool { return calls.Load() == 2 })
}

func TestTCSUPHOT009RecordDoesNotWaitForRecoveryIO(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	block := make(chan struct{})
	s := testSupervisor(t, clock, Owners{
		RefreshRoute: func(ctx context.Context) error {
			select {
			case <-block:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	s.Recover(Outcome{Kind: OutcomeRouteFailure})
	start := time.Now()
	for i := 0; i < 1000; i++ {
		s.Record(Outcome{Kind: OutcomeDestinationFailure})
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Record blocked on recovery I/O: %v", elapsed)
	}
	close(block)
}

func TestTCSUPOWNER010OutcomesRequestOnlyScopedOwnerActions(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	var route, auth, idle atomic.Int32
	s := testSupervisor(t, clock, Owners{
		RefreshRoute:        func(context.Context) error { route.Add(1); return nil },
		RefreshAuth:         func(context.Context) error { auth.Add(1); return nil },
		CloseIdleTransports: func(context.Context) error { idle.Add(1); return nil },
	})
	s.Recover(Outcome{Kind: OutcomeRouteFailure})
	s.Recover(Outcome{Kind: OutcomeAuthExhausted})
	s.Recover(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	s.Recover(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	waitUntil(t, func() bool {
		return route.Load() == 1 && auth.Load() == 1 && idle.Load() == 1
	})
	if got := s.Status().HealthEntries; got != 1 {
		t.Fatalf("health entries = %d, want one proxy penalty", got)
	}
}

func TestTCSUPNET011SchedulingGapStartsOneNetworkEpoch(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	var route, auth, idle, dns atomic.Int32
	s := testSupervisor(t, clock, Owners{
		RefreshRoute:        func(context.Context) error { route.Add(1); return nil },
		RefreshAuth:         func(context.Context) error { auth.Add(1); return nil },
		CloseIdleTransports: func(context.Context) error { idle.Add(1); return nil },
		ClearNetworkDNS:     func(context.Context) error { dns.Add(1); return nil },
	})
	s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	s.Tick(clock.Now())
	clock.Advance(time.Second)
	s.Tick(clock.Now())
	if got := s.Status().NetworkEpoch; got != 0 {
		t.Fatalf("ordinary tick epoch = %d", got)
	}
	clock.Advance(networkGapThreshold + time.Second)
	s.Tick(clock.Now())
	waitUntil(t, func() bool {
		return route.Load() == 1 && auth.Load() == 1 && idle.Load() == 1 && dns.Load() == 1
	})
	status := s.Status()
	if status.NetworkEpoch != 1 {
		t.Fatalf("network epoch = %d, want 1", status.NetworkEpoch)
	}
	if status.HealthEntries != 0 {
		t.Fatalf("health entries after epoch = %d", status.HealthEntries)
	}
}

func TestTCSUPNET012CorrelatedFailureEpochIsBounded(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{})
	for i := 0; i < correlatedFailureThreshold*2; i++ {
		s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	}
	if got := s.Status().NetworkEpoch; got != 0 {
		t.Fatalf("single-proxy failures created epoch %d", got)
	}
	for i := 0; i < correlatedFailureThreshold; i++ {
		key := ProxyKey("proxy-a")
		if i%2 == 1 {
			key = "proxy-b"
		}
		s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: key})
	}
	if got := s.Status().NetworkEpoch; got != 1 {
		t.Fatalf("correlated failures epoch = %d, want 1", got)
	}
	for i := 0; i < correlatedFailureThreshold*2; i++ {
		s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: ProxyKey("proxy-c")})
	}
	if got := s.Status().NetworkEpoch; got != 1 {
		t.Fatalf("epoch ignored cooldown: got %d", got)
	}
}

func TestTCSUPSTATUS013SnapshotAndProgressAreIndependent(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{
		RefreshRoute: func(context.Context) error { return context.DeadlineExceeded },
	})
	before := s.Status()
	s.Tick(clock.Now())
	after := s.Status()
	if after.ProgressSequence <= before.ProgressSequence {
		t.Fatalf("progress did not advance: before=%d after=%d", before.ProgressSequence, after.ProgressSequence)
	}
	if after.LastProgress.IsZero() {
		t.Fatal("LastProgress is zero")
	}
	copy := after
	copy.NetworkEpoch = 999
	if s.Status().NetworkEpoch == 999 {
		t.Fatal("status mutation changed supervisor state")
	}
}

func TestTCSUPFATAL015OnlyRepeatedInternalFailureRequestsFatal(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{})
	for _, kind := range []OutcomeKind{
		OutcomeRouteFailure, OutcomeProxyDialFailure, OutcomeAuthExhausted,
		OutcomeDestinationFailure, OutcomeClientCancelled,
	} {
		for i := 0; i < internalFatalThreshold+2; i++ {
			s.Record(Outcome{Kind: kind, Proxy: "proxy-a"})
		}
	}
	if s.Status().FatalRequested {
		t.Fatal("external/runtime outage requested fatal escalation")
	}
	for i := 0; i < internalFatalThreshold; i++ {
		s.Record(Outcome{Kind: OutcomeInternalFailure})
	}
	if !s.Status().FatalRequested {
		t.Fatal("repeated internal failure did not request fatal escalation")
	}
	select {
	case signal := <-s.FatalSignals():
		if signal.Reason == "" {
			t.Fatal("fatal signal missing reason")
		}
	case <-time.After(time.Second):
		t.Fatal("fatal signal not emitted")
	}
}
