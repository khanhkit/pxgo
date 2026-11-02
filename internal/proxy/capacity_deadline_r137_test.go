package proxy

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

// The absolute deadline applies even when a new caller is stalled before
// acquiring the admission mutex. The old select-only check would return
// success after a late unlock because it had no timer until contention.
func TestR137AbsoluteDeadlineRejectsSlowMutexBeforeAdmission(t *testing.T) {
	const rounds = 12
	for round := 0; round < rounds; round++ {
		g := r67OneOccupiedGovernor()
		g.mu.Lock()
		g.active = 0
		done := make(chan struct{})
		started := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			close(started)
			result <- g.acquire(done, 10*time.Millisecond)
		}()
		<-started
		runtime.Gosched() // allow the waiter to reach the already-held mutex
		time.Sleep(35 * time.Millisecond)
		g.mu.Unlock()
		select {
		case err := <-result:
			var expired capacityWaitTimeout
			if !errors.As(err, &expired) {
				t.Fatalf("round %d: permit admitted after absolute expiry, error=%v", round, err)
			}
		case <-time.After(1 * time.Second):
			t.Fatalf("round %d: waiter hung after mutex unlock", round)
		}
		g.mu.Lock()
		if g.active != 0 || g.waiting != 0 || len(g.waitQueue) != 0 {
			t.Fatalf("round %d: late permit or waiter leak: active=%d waiting=%d queue=%d", round, g.active, g.waiting, len(g.waitQueue))
		}
		g.mu.Unlock()
		close(done)
		g.close()
	}
}
