//go:build linux

package proxy

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestR91QueuedFIFOIsPausedBySensorOutageAndResumesOnFreshMemory(t *testing.T) {
	cfg := config.Default()
	cfg.MaxConnections = 1
	g := newCapacityGovernor(modeAuto, cfg)
	defer g.close()
	now := time.Unix(450000, 0)
	measurement := func(slots int) capacitySample {
		return capacitySample{available: true, freeFD: 9000, totalFD: 10000,
			linuxMemoryConstrained: true, linuxMemoryFreeSlots: slots}
	}
	g.sample(measurement(1), now)
	done := make(chan struct{})
	if err := g.acquire(done, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	waitFor := func(n int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			g.mu.Lock()
			waiting := g.waiting
			g.mu.Unlock()
			if waiting == n {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("expected %d registered FIFO waiters", n)
	}
	result := make(chan int, 2)
	spawn := func(id int) {
		go func() {
			err := g.acquire(done, 1500*time.Millisecond)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					result <- -id
					return
				}
				result <- -id
				return
			}
			result <- id
			g.release()
		}()
	}
	spawn(1)
	waitFor(1)
	spawn(2)
	waitFor(2)
	g.sample(capacitySample{reason: "fd sampling lost"}, now.Add(time.Second))
	g.release()
	select {
	case resultID := <-result:
		t.Fatalf("waiter %d escaped during unknown cgroup/FD telemetry", resultID)
	case <-time.After(25 * time.Millisecond):
	}
	g.sample(measurement(2), now.Add(2*time.Second))
	select {
	case id := <-result:
		if id != 1 {
			t.Fatalf("FIFO violated after fresh memory recovery; got %d", id)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("first FIFO waiter not resumed after valid sensor")
	}
	select {
	case id := <-result:
		t.Fatalf("second waiter minted headroom from first release: %d", id)
	case <-time.After(20 * time.Millisecond):
	}
	g.sample(measurement(3), now.Add(3*time.Second))
	select {
	case id := <-result:
		if id != 2 {
			t.Fatalf("second FIFO waiter order=%d", id)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("second FIFO waiter did not progress after valid new headroom")
	}
	if s := g.snapshot(); s.ActiveDownstream != 0 {
		t.Fatalf("physical permit leak after queued recovery: %+v", s)
	}
}
