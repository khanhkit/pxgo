package proxy

import (
	"errors"
	"github.com/khanhkit/pxgo/internal/config"
	"net"
	"testing"
	"time"
)

func r67OneOccupiedGovernor() *capacityGovernor {
	g := newCapacityGovernor(modeAuto, config.Default())
	g.mu.Lock()
	g.soft = 1
	g.hard = 1
	g.recomputeLocked()
	g.active = 1
	g.mu.Unlock()
	return g
}

func TestR67CanceledFirstWaiterYieldsToSecond(t *testing.T) {
	g := r67OneOccupiedGovernor()
	defer g.close()
	aDone := make(chan struct{})
	bDone := make(chan struct{})
	defer close(bDone)
	aResult := make(chan error, 1)
	bResult := make(chan error, 1)
	go func() { aResult <- g.acquire(aDone, time.Second) }()
	waitForR67Waiters(t, g, 1)
	go func() { bResult <- g.acquire(bDone, time.Second) }()
	waitForR67Waiters(t, g, 2)
	close(aDone)
	select {
	case err := <-aResult:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("head cancellation err=%v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("head waiter did not cancel")
	}
	waitForR67Waiters(t, g, 1)
	g.release()
	select {
	case err := <-bResult:
		if err != nil {
			t.Fatalf("next waiter after cancel err=%v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("next waiter stuck after head cancel")
	}
	g.mu.Lock()
	active, queued := g.active, len(g.waitQueue)
	g.mu.Unlock()
	if active != 1 || queued != 0 {
		t.Fatalf("leak: active=%d queued=%d", active, queued)
	}
}

func TestR67ExpiredFirstWaiterYieldsToSecond(t *testing.T) {
	g := r67OneOccupiedGovernor()
	defer g.close()
	done := make(chan struct{})
	defer close(done)
	aResult := make(chan error, 1)
	bResult := make(chan error, 1)
	go func() { aResult <- g.acquire(done, 40*time.Millisecond) }()
	waitForR67Waiters(t, g, 1)
	go func() { bResult <- g.acquire(done, 800*time.Millisecond) }()
	waitForR67Waiters(t, g, 2)
	select {
	case err := <-aResult:
		var timeout capacityWaitTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("head timeout err=%v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("head waiter deadline missed")
	}
	waitForR67Waiters(t, g, 1)
	g.release()
	select {
	case err := <-bResult:
		if err != nil {
			t.Fatalf("next waiter after timeout err=%v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("next waiter stuck after head timeout")
	}
	g.mu.Lock()
	active, queued, waiting := g.active, len(g.waitQueue), g.waiting
	g.mu.Unlock()
	if active != 1 || queued != 0 || waiting != 0 {
		t.Fatalf("leak: active=%d queued=%d waiting=%d", active, queued, waiting)
	}
}
