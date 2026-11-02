package proxy

import (
	"errors"
	"testing"
	"time"
)

func TestR68ExpiredAdmissionDeadlineCannotWinLateRelease(t *testing.T) {
	lateAdmits := 0
	earlyTimeouts := 0
	for round := 0; round < 48; round++ {
		g := r67OneOccupiedGovernor()
		done := make(chan struct{})
		result := make(chan error, 1)
		go func() { result <- g.acquire(done, 20*time.Millisecond) }()
		// An absolute deadline now begins at the *entry* of acquire, before
		// the waiter can register under g.mu. Under a busy/race-instrumented
		// Windows runner the call can correctly time out BEFORE registration.
		// Distinguish that valid outcome from a late grant; do not let the
		// generic FIFO helper mistake it for a permanently missing waiter.
		registered := false
		earlyTimeout := false
		until := time.Now().Add(4 * time.Second)
		for time.Now().Before(until) {
			g.mu.Lock()
			registered = g.waiting == 1
			g.mu.Unlock()
			if registered {
				break
			}
			select {
			case err := <-result:
				var timedOut capacityWaitTimeout
				if !errors.As(err, &timedOut) {
					t.Fatalf("round %d unregistered acquire err=%v (expected absolute timeout)", round, err)
				}
				earlyTimeout = true
			default:
			}
			if earlyTimeout {
				break
			}
			time.Sleep(200 * time.Microsecond)
		}
		if !registered {
			if !earlyTimeout {
				t.Fatalf("round %d neither registered waiter nor bounded early timeout", round)
			}
			g.mu.Lock()
			active, waiting, queued := g.active, g.waiting, len(g.waitQueue)
			g.mu.Unlock()
			if active != 1 || waiting != 0 || queued != 0 {
				t.Fatalf("round %d early timeout leaked permit/queue: active=%d waiting=%d queue=%d", round, active, waiting, queued)
			}
			earlyTimeouts++
			close(done)
			g.close()
			continue
		}
		g.mu.Lock()
		time.Sleep(45 * time.Millisecond)
		g.active = 0
		g.notifyLocked()
		g.mu.Unlock()
		select {
		case err := <-result:
			if err == nil {
				lateAdmits++
			}
			if err != nil {
				var timedOut capacityWaitTimeout
				if !errors.As(err, &timedOut) {
					t.Fatalf("round %d err=%v", round, err)
				}
			}
		case <-time.After(300 * time.Millisecond):
			t.Fatalf("round %d waiter did not complete promptly", round)
		}
		close(done)
		g.close()
	}
	if lateAdmits != 0 {
		t.Fatalf("R68_ABSOLUTE_DEADLINE_BREACHED: %d/48 acquisition(s) succeeded only AFTER a hard 20ms deadline elapsed", lateAdmits)
	}
	t.Logf("R139_R68_HARNESS_RESULT rounds=48 early_absolute_timeouts=%d late_admits=0", earlyTimeouts)
}
