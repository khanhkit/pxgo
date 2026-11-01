package proxy

import (
	"testing"
	"time"
)

func TestR67FIFO128WaitersNoStarvationOrPermitLeak(t *testing.T) {
	const n = 128
	g := r67OneOccupiedGovernor()
	defer g.close()
	done := make(chan struct{})
	defer close(done)
	received := make(chan struct {
		index int
		err   error
	}, n)
	for i := 0; i < n; i++ {
		index := i
		go func() {
			received <- struct {
				index int
				err   error
			}{index, g.acquire(done, 8*time.Second)}
		}()
		waitForR67Waiters(t, g, i+1)
	}
	for i := 0; i < n; i++ {
		g.release()
		select {
		case result := <-received:
			if result.err != nil || result.index != i {
				t.Fatalf("R67_FIFO_128_STARVATION first[%d]=%d err=%v", i, result.index, result.err)
			}
		case <-time.After(750 * time.Millisecond):
			t.Fatalf("R67_FIFO_128_STARVATION waiter[%d] not admitted promptly", i)
		}
	}
	g.release()
	g.mu.Lock()
	active, waiting, queued := g.active, g.waiting, len(g.waitQueue)
	g.mu.Unlock()
	if active != 0 || waiting != 0 || queued != 0 {
		t.Fatalf("R67_FIFO_128_LEAK active=%d waiting=%d queued=%d", active, waiting, queued)
	}
	t.Log("R67_FIFO_128_RESULT ordered=128/128 physical_after=0 waiting_after=0 no_starvation=true")
}
