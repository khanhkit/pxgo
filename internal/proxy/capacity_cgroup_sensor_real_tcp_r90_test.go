//go:build linux

package proxy

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestR90RealTCPStaleSensorFencesOnlyNewSockets(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newDynamicAdmissionListener(raw, g, 50*time.Millisecond)
	defer ln.Close()
	dial := func() net.Conn {
		t.Helper()
		c, e := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	existingClient := dial()
	defer existingClient.Close()
	existingServer, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer existingServer.Close()
	now := time.Unix(303000, 0)
	finite := capacitySample{
		available: true, totalFD: 21000, freeFD: 20000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 5,
	}
	g.sample(finite, now)
	g.sample(capacitySample{reason: "fd count inaccessible"}, now.Add(time.Second))
	if snap := g.snapshot(); snap.ActiveDownstream != 1 || snap.EffectiveLimit != 0 {
		t.Fatalf("finite cgroup outage not fenced: %+v", snap)
	}
	// Existing duplex TCP traffic must not be evicted by an UNKNOWN sample.
	done := make(chan error, 1)
	_ = existingClient.SetDeadline(time.Now().Add(time.Second))
	go func() {
		msg := make([]byte, 4)
		_, e := io.ReadFull(existingServer, msg)
		if e == nil {
			_, e = existingServer.Write(msg)
		}
		done <- e
	}()
	_, err = existingClient.Write([]byte("live"))
	if err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, 4)
	if _, err = io.ReadFull(existingClient, echoed); err != nil || string(echoed) != "live" {
		t.Fatalf("existing transport not usable: %q %v", echoed, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	extraClient := dial()
	defer extraClient.Close()
	if conn, e := ln.Accept(); e == nil {
		_ = conn.Close()
		t.Fatal("unverified extra physical socket was admitted")
	} else {
		var timeout net.Error
		if !errors.As(e, &timeout) || !timeout.Timeout() {
			t.Fatalf("expected admission timeout, got %v", e)
		}
	}
	_ = existingServer.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 || s.EffectiveLimit != 0 {
		t.Fatalf("release reopened stale memory headroom: %+v", s)
	}
	g.sample(finite, now.Add(3*time.Second))
	recoveredClient := dial()
	defer recoveredClient.Close()
	recovered, err := ln.Accept()
	if err != nil {
		t.Fatalf("fresh sample failed recovery: %v", err)
	}
	_ = recovered.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 {
		t.Fatalf("physical permit leak: %+v", s)
	}
}
