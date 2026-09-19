package guardian

import (
	"testing"
	"time"
)

func TestTCGUARDWATCH010RegularProgressKeepsReadyWorkerHealthy(t *testing.T) {
	base := time.Unix(1000, 0)
	w := NewWatchdog(20 * time.Second)
	w.Ready(base)
	for i := 1; i <= 10; i++ {
		now := base.Add(time.Duration(i) * 2 * time.Second)
		w.Beat(uint64(i), now)
		if w.Check(now) {
			t.Fatalf("healthy beat %d marked hung", i)
		}
	}
}

func TestTCGUARDWATCH011HangOnlyAfterReadyAndGrace(t *testing.T) {
	base := time.Unix(1000, 0)
	w := NewWatchdog(20 * time.Second)
	if w.Check(base.Add(time.Minute)) {
		t.Fatal("starting worker marked hung")
	}
	w.Ready(base)
	if w.Check(base.Add(20 * time.Second)) {
		t.Fatal("worker hung at exact grace boundary")
	}
	if !w.Check(base.Add(20*time.Second + time.Nanosecond)) {
		t.Fatal("missing progress beyond grace not marked hung")
	}
}

func TestTCGUARDWATCH012ObserverGapGrantsFreshGrace(t *testing.T) {
	base := time.Unix(1000, 0)
	w := NewWatchdog(20 * time.Second)
	w.Ready(base)
	w.Beat(1, base.Add(time.Second))
	if w.Check(base.Add(2 * time.Second)) {
		t.Fatal("healthy worker marked hung")
	}

	resumed := base.Add(2*time.Second + 45*time.Second)
	if w.Check(resumed) {
		t.Fatal("parent scheduling/suspend gap killed worker immediately")
	}
	if w.Check(resumed.Add(20 * time.Second)) {
		t.Fatal("fresh resume grace ended too early")
	}
	if !w.Check(resumed.Add(20*time.Second + time.Nanosecond)) {
		t.Fatal("worker without resumed progress not marked hung after fresh grace")
	}
}

func TestTCGUARDWATCH010DuplicateBeatDoesNotFakeProgress(t *testing.T) {
	base := time.Unix(1000, 0)
	w := NewWatchdog(20 * time.Second)
	w.Ready(base)
	w.Beat(5, base.Add(time.Second))
	w.Beat(5, base.Add(10*time.Second))
	if w.Check(base.Add(10 * time.Second)) {
		t.Fatal("active observer marked worker hung too early")
	}
	if !w.Check(base.Add(21*time.Second + time.Nanosecond)) {
		t.Fatal("duplicate heartbeat sequence incorrectly refreshed progress")
	}
}
