package proxy

import (
	"errors"
	"testing"
	"time"
)

func TestR68ExpiredAdmissionDeadlineCannotWinLateRelease(t *testing.T) {
	lateAdmits := 0
	for round := 0; round < 48; round++ {
		g := r67OneOccupiedGovernor()
		done := make(chan struct{})
		result := make(chan error, 1)
		go func() { result <- g.acquire(done, 20*time.Millisecond) }()
		waitForR67Waiters(t, g, 1)
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
}
