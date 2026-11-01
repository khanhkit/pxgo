package proxy

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestR71FIFOQueueDrainsOnDynamicWindowGrowth(t *testing.T) {
	g := r67OneOccupiedGovernor()
	defer g.close()
	done := make(chan struct{})
	defer close(done)
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { results <- g.acquire(done, time.Second) }()
		waitForR67Waiters(t, g, i+1)
	}
	g.mu.Lock()
	g.soft = 4
	g.hard = 4
	g.recomputeLocked()
	g.mu.Unlock()
	for i := 0; i < 3; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("FIFO waiter[%d] after growth err=%v", i, err)
			}
		case <-time.After(400 * time.Millisecond):
			t.Fatalf("queued waiter[%d] stuck despite 3 newly available permits", i)
		}
	}
	g.mu.Lock()
	active, waiting, queue := g.active, g.waiting, len(g.waitQueue)
	g.mu.Unlock()
	if active != 4 || waiting != 0 || queue != 0 {
		t.Fatalf("window growth wrong active=%d waiting=%d queued=%d", active, waiting, queue)
	}
	for i := 0; i < 4; i++ {
		g.release()
	}
	if snap := g.snapshot(); snap.ActiveDownstream != 0 {
		t.Fatalf("window growth leaked permits: %+v", snap)
	}
}

func TestR71GovernorShutdownDrainsAllWaitingFIFO(t *testing.T) {
	g := r67OneOccupiedGovernor()
	done := make(chan struct{})
	defer close(done)
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() { results <- g.acquire(done, 2*time.Second) }()
		waitForR67Waiters(t, g, i+1)
	}
	g.close()
	for i := 0; i < 20; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("shutdown waiter[%d] err=%v", i, err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("shutdown waiter[%d] remains blocked", i)
		}
	}
	g.release()
	g.mu.Lock()
	active, waiting, queue := g.active, g.waiting, len(g.waitQueue)
	g.mu.Unlock()
	if active != 0 || waiting != 0 || queue != 0 {
		t.Fatalf("shutdown leaked active=%d waiting=%d queue=%d", active, waiting, queue)
	}
}
