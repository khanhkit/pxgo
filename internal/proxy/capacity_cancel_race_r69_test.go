package proxy

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestR69CancellationWinsConcurrentPhysicalRelease(t *testing.T) {
	for round := 0; round < 100; round++ {
		g := r67OneOccupiedGovernor()
		done := make(chan struct{})
		result := make(chan error, 1)
		go func() { result <- g.acquire(done, time.Second) }()
		waitForR67Waiters(t, g, 1)
		// Serialize release and close with the governor mutex. The close signal
		// is already visible before the next chance to acquire the physical slot.
		g.mu.Lock()
		close(done)
		g.active = 0
		g.notifyLocked()
		g.mu.Unlock()
		select {
		case err := <-result:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("R69_CANCEL_RACE round=%d err=%v want=net.ErrClosed", round, err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("round=%d canceled waiter did not finish", round)
		}
		g.mu.Lock()
		active, waiting, queue := g.active, g.waiting, len(g.waitQueue)
		g.mu.Unlock()
		g.close()
		if active != 0 || waiting != 0 || queue != 0 {
			t.Fatalf("R69_CANCEL_LEAK round=%d active=%d waiting=%d queue=%d", round, active, waiting, queue)
		}
	}
}
