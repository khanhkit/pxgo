package supervisor

import (
	"reflect"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func testSupervisor(t *testing.T, clock *fakeClock, owners Owners) *Supervisor {
	t.Helper()
	s := newWithClock(owners, clock.Now)
	t.Cleanup(func() { s.Close() })
	return s
}

func candidate(key string) Candidate { return Candidate{Key: ProxyKey(key)} }
func directCandidate() Candidate     { return Candidate{Key: ProxyKey("direct://DIRECT:80"), Direct: true} }

func TestTCSUPHEALTH002FirstLocalFailurePreservesOrder(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{})
	authoritative := []Candidate{candidate("proxy-a"), candidate("proxy-b")}
	s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	if got := s.Order(authoritative); !reflect.DeepEqual(got, authoritative) {
		t.Fatalf("order after first failure = %#v, want %#v", got, authoritative)
	}
}

func TestTCSUPHEALTH003RepeatedFailureCoolsAndSuccessResets(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{})
	authoritative := []Candidate{candidate("proxy-a"), candidate("proxy-b")}
	s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: "proxy-a"})
	wantCooled := []Candidate{candidate("proxy-b"), candidate("proxy-a")}
	if got := s.Order(authoritative); !reflect.DeepEqual(got, wantCooled) {
		t.Fatalf("cooled order = %#v, want %#v", got, wantCooled)
	}
	s.Record(Outcome{Kind: OutcomeSuccess, Proxy: "proxy-a"})
	if got := s.Order(authoritative); !reflect.DeepEqual(got, authoritative) {
		t.Fatalf("order after success = %#v, want %#v", got, authoritative)
	}
}

func TestTCSUPHEALTH004NonLocalOutcomesNeverCool(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{})
	authoritative := []Candidate{candidate("proxy-a"), candidate("proxy-b")}
	for _, kind := range []OutcomeKind{OutcomeDestinationFailure, OutcomeClientCancelled, OutcomeAuthExhausted, OutcomeRouteFailure} {
		for i := 0; i < 4; i++ {
			s.Record(Outcome{Kind: kind, Proxy: "proxy-a"})
		}
		if got := s.Order(authoritative); !reflect.DeepEqual(got, authoritative) {
			t.Fatalf("%v changed order to %#v", kind, got)
		}
	}
}

func TestTCSUPROUTE005DirectSlotAndMembershipArePreserved(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{})
	authoritative := []Candidate{candidate("proxy-a"), directCandidate(), candidate("proxy-b")}
	s.Record(Outcome{Kind: OutcomeProxyTLSFailure, Proxy: "proxy-a"})
	s.Record(Outcome{Kind: OutcomeProxyTLSFailure, Proxy: "proxy-a"})
	want := []Candidate{candidate("proxy-b"), directCandidate(), candidate("proxy-a")}
	got := s.Order(authoritative)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %#v, want %#v", got, want)
	}
	if len(got) != len(authoritative) {
		t.Fatalf("membership size changed: got %d want %d", len(got), len(authoritative))
	}
}

func TestTCSUPHEALTH006AllCooledFallsBackToAuthoritativeOrder(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{})
	authoritative := []Candidate{candidate("proxy-a"), candidate("proxy-b")}
	for _, key := range []ProxyKey{"proxy-a", "proxy-b"} {
		s.Record(Outcome{Kind: OutcomeProxyProtocolFailure, Proxy: key})
		s.Record(Outcome{Kind: OutcomeProxyProtocolFailure, Proxy: key})
	}
	if got := s.Order(authoritative); !reflect.DeepEqual(got, authoritative) {
		t.Fatalf("all cooled order = %#v, want authoritative %#v", got, authoritative)
	}
}

func TestTCSUPBOUND007TransientStateIsBoundedAndResettable(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	s := testSupervisor(t, clock, Owners{})
	for i := 0; i < maxHealthEntries+50; i++ {
		key := ProxyKey("proxy-" + string(rune(0x1000+i)))
		s.Record(Outcome{Kind: OutcomeProxyDialFailure, Proxy: key})
	}
	if got := s.Status().HealthEntries; got > maxHealthEntries {
		t.Fatalf("health entries = %d, max %d", got, maxHealthEntries)
	}
	s.ResetTransient()
	if got := s.Status().HealthEntries; got != 0 {
		t.Fatalf("health entries after reset = %d", got)
	}
	fresh := testSupervisor(t, clock, Owners{})
	if got := fresh.Status().HealthEntries; got != 0 {
		t.Fatalf("fresh supervisor inherited %d entries", got)
	}
}
