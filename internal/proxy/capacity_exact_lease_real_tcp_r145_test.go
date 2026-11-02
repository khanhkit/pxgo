//go:build linux

package proxy

import (
	"github.com/khanhkit/pxgo/internal/config"
	"net"
	"testing"
	"time"
)

// Integrates the PRODUCTION dynamic admissionListener and admissionConn.Close
// exact-ID path rather than directly calling governor acquireLease/releaseLease.
func TestR145NativeRealTCPOutOfOrderLeaseClosureRetainsNewerMemoryDebt(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	now := time.Now()
	s := capacitySample{available: true, totalFD: 10000, freeFD: 9000}
	g.sample(s, now)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newDynamicAdmissionListener(raw, g, 500*time.Millisecond)
	defer ln.Close()
	acceptOne := func() (net.Conn, net.Conn) {
		type response struct {
			conn net.Conn
			err  error
		}
		out := make(chan response, 1)
		go func() { c, e := ln.Accept(); out <- response{c, e} }()
		client, e := net.DialTimeout("tcp", raw.Addr().String(), 500*time.Millisecond)
		if e != nil {
			t.Fatal(e)
		}
		select {
		case r := <-out:
			if r.err != nil {
				_ = client.Close()
				t.Fatal(r.err)
			}
			return r.conn, client
		case <-time.After(time.Second):
			_ = client.Close()
			t.Fatal("accepted socket blocked unexpectedly")
			return nil, nil
		}
	}
	old, oldClient := acceptOne()
	defer oldClient.Close()
	s.linuxMemoryConstrained = true
	s.linuxMemoryFreeSlots = 10
	g.sample(s, now.Add(time.Second))
	newer, newClient := acceptOne()
	defer newer.Close()
	defer newClient.Close()
	if g.memoryActiveDebt != 1 {
		t.Fatalf("newer tracked debt setup=%d", g.memoryActiveDebt)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	_ = old.Close() // actual admissionConn CloseOnce: do not double-release
	g.mu.Lock()
	active, pending, retired := g.active, g.memoryActiveDebt, g.memoryClosedDebt
	g.mu.Unlock()
	if active != 1 || pending != 1 || retired != 0 {
		t.Fatalf("older real TCP Close settled newer memory lease: active=%d pending=%d retired=%d", active, pending, retired)
	}
	s.linuxMemoryFreeSlots = 9
	g.sample(s, now.Add(2*time.Second))
	s.linuxMemoryFreeSlots = 10
	g.sample(s, now.Add(3*time.Second))
	g.mu.Lock()
	pending, retired = g.memoryActiveDebt, g.memoryClosedDebt
	g.mu.Unlock()
	if pending != 1 || retired != 0 {
		t.Fatalf("external recovery falsely retired real socket debt: activeDebt=%d retired=%d", pending, retired)
	}
	if err := newer.Close(); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	active, pending, retired = g.active, g.memoryActiveDebt, g.memoryClosedDebt
	g.mu.Unlock()
	if active != 0 || pending != 0 || retired != 1 {
		t.Fatalf("newer Close misaccounted: active=%d pending=%d retired=%d", active, pending, retired)
	}
}
