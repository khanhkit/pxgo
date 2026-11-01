//go:build linux

package proxy

import (
	"errors"
	"github.com/khanhkit/pxgo/internal/config"
	"io"
	"net"
	"testing"
	"time"
)

func TestR85RealTCPPartialMemoryBudgetNeverReusesStaleHeadroom(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newDynamicAdmissionListener(raw, g, 35*time.Millisecond)
	defer ln.Close()
	dial := func() net.Conn {
		t.Helper()
		client, e := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		return client
	}
	oldClient := dial()
	defer oldClient.Close()
	oldConn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	measured := capacitySample{available: true, freeFD: 9000, totalFD: 10000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 1}
	now := time.Unix(222000, 0)
	g.sample(measured, now)
	_ = oldConn.Close() // existed BEFORE the cgroup measurement
	if snap := g.snapshot(); snap.ActiveDownstream != 0 {
		t.Fatalf("old physical permit leaked %+v", snap)
	}
	newClient := dial()
	defer newClient.Close()
	first, err := ln.Accept()
	if err != nil {
		t.Fatalf("the one verified NEW memory slot was wrongly denied: %v", err)
	}
	defer first.Close()
	// Existing newly admitted traffic remains functional when headroom is gone.
	msg := []byte("test")
	if _, err = newClient.Write(msg); err != nil {
		t.Fatal(err)
	}
	recv := make([]byte, len(msg))
	_ = first.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = io.ReadFull(first, recv); err != nil || string(recv) != string(msg) {
		t.Fatalf("new connection was evicted or corrupted: read=%q err=%v", recv, err)
	}
	deniedClient := dial()
	defer deniedClient.Close()
	if denied, err := ln.Accept(); err == nil {
		_ = denied.Close()
		t.Fatal("SECOND_REAL_TCP_ADMITTED_FROM_ONE_SLOT_STALE_MEMORY_SAMPLE")
	} else {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("blocked wait must time out, got %v", err)
		}
	}
	_ = first.Close()
	if snap := g.snapshot(); snap.ActiveDownstream != 0 || snap.EffectiveLimit != 0 {
		t.Fatalf("closing the new socket manufactured another slot: %+v", snap)
	}
	// New intake requires MEASURED extra headroom, not a repeated
	// identical positive reading while the old virtual debt is pending.
	fresh := measured
	fresh.linuxMemoryFreeSlots = 2
	g.sample(fresh, now.Add(4*time.Second))
	recoveredClient := dial()
	defer recoveredClient.Close()
	recovered, err := ln.Accept()
	if err != nil {
		t.Fatalf("fresh verified budget failed to reopen: %v", err)
	}
	_ = recovered.Close()
	if snap := g.snapshot(); snap.ActiveDownstream != 0 {
		t.Fatalf("recovered socket leaked: %+v", snap)
	}
}
