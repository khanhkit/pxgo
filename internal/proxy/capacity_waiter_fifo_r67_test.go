package proxy

import (
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func waitForR67Waiters(t *testing.T, g *capacityGovernor, want int) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		n := g.waiting
		g.mu.Unlock()
		if n == want {
			return
		}
		time.Sleep(200 * time.Microsecond)
	}
	t.Fatalf("timeout waiting for %d registered waiter(s)", want)
}

func TestR67FirstInFirstOutWaiterFairness(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	g.mu.Lock()
	g.soft = 1
	g.hard = 1
	g.recomputeLocked()
	g.active = 1
	g.mu.Unlock()
	defer g.close()
	done := make(chan struct{})
	defer close(done)
	reversals := 0
	for round := 0; round < 80; round++ {
		results := make(chan int, 2)
		go func() {
			if err := g.acquire(done, 3*time.Second); err != nil {
				results <- (-10)
			} else {
				results <- 1
			}
		}()
		waitForR67Waiters(t, g, 1)
		go func() {
			if err := g.acquire(done, 3*time.Second); err != nil {
				results <- (-20)
			} else {
				results <- 2
			}
		}()
		waitForR67Waiters(t, g, 2)
		g.release()
		first := <-results
		g.release()
		second := <-results
		if first == 2 {
			reversals++
		}
		if first < 0 || second < 0 || first == second {
			t.Fatalf("round=%d arrivals=%d,%d", round, first, second)
		}
	}
	if reversals != 0 {
		t.Fatalf("R67_FAIRNESS_VIOLATION newer waiter overtook registered earlier waiter %d/80 trials", reversals)
	}
}
