package proxy

import (
	"net"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

type r67AcceptResult struct {
	which int
	conn  net.Conn
	err   error
}

func TestR67RealTwoListenerFIFOAndPhysicalPermitConservation(t *testing.T) {
	for trial := 0; trial < 25; trial++ {
		g := newCapacityGovernor(modeAuto, config.Default())
		g.mu.Lock()
		g.soft = 1
		g.hard = 1
		g.recomputeLocked()
		g.active = 1
		g.mu.Unlock()
		rawA, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		rawB, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = rawA.Close()
			t.Fatal(err)
		}
		a := newDynamicAdmissionListener(rawA, g, 2*time.Second)
		b := newDynamicAdmissionListener(rawB, g, 2*time.Second)
		accepted := make(chan r67AcceptResult, 2)
		go func() { c, e := a.Accept(); accepted <- r67AcceptResult{which: 1, conn: c, err: e} }()
		clientA, err := net.DialTimeout("tcp", a.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		waitForR67Waiters(t, g, 1)
		go func() { c, e := b.Accept(); accepted <- r67AcceptResult{which: 2, conn: c, err: e} }()
		clientB, err := net.DialTimeout("tcp", b.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		waitForR67Waiters(t, g, 2)
		g.release()
		var first r67AcceptResult
		select {
		case first = <-accepted:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("first listener did not make progress")
		}
		if first.err != nil || first.which != 1 {
			t.Fatalf("trial=%d FIFO breached: first=%+v", trial, first)
		}
		_ = first.conn.Close()
		var second r67AcceptResult
		select {
		case second = <-accepted:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("second listener did not make progress")
		}
		if second.err != nil || second.which != 2 {
			t.Fatalf("trial=%d FIFO breached: second=%+v", trial, second)
		}
		_ = second.conn.Close()
		_ = clientA.Close()
		_ = clientB.Close()
		_ = a.Close()
		_ = b.Close()
		g.mu.Lock()
		active, waiting, queue := g.active, g.waiting, len(g.waitQueue)
		g.mu.Unlock()
		g.close()
		if active != 0 || waiting != 0 || queue != 0 {
			t.Fatalf("trial=%d leaked physical/admission tokens active=%d waiting=%d queue=%d", trial, active, waiting, queue)
		}
	}
	t.Log("R67 real TCP two-listener FIFO and single global physical capacity 25/25 PASS")
}
