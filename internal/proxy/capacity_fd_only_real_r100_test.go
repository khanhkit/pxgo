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

// Real physical sockets are carried across an FD-only sensor blackout.
// Only NEW physical intake is fenced; older accepted traffic must survive.
func TestR100RealTCPFDOnlyOutagePreservesExistingAndResumes(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newDynamicAdmissionListener(raw, g, 40*time.Millisecond)
	defer ln.Close()
	healthy := capacitySample{available: true, totalFD: 10000, freeFD: 9000}
	now := time.Unix(520000, 0)
	g.sample(healthy, now)
	dial := func() net.Conn {
		t.Helper()
		c, e := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	client := dial()
	defer client.Close()
	existing, e := ln.Accept()
	if e != nil {
		t.Fatal(e)
	}
	defer existing.Close()
	g.sample(capacitySample{reason: "unreadable-/proc/self/fd"}, now.Add(time.Second))
	if s := g.snapshot(); s.ActiveDownstream != 1 || s.EffectiveLimit != 0 {
		t.Fatalf("FD-only outage opened new intake or evicted existing: %+v", s)
	}
	packet := []byte("alive")
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_ = existing.SetDeadline(time.Now().Add(time.Second))
	if _, e = client.Write(packet); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, len(packet))
	if _, e = io.ReadFull(existing, buf); e != nil || string(buf) != string(packet) {
		t.Fatalf("prior TCP packet failed during outage: %q err=%v", buf, e)
	}
	deniedClient := dial()
	defer deniedClient.Close()
	if c, e := ln.Accept(); e == nil {
		_ = c.Close()
		t.Fatal("new TCP accepted during unavailable FD-only sensor")
	} else {
		var timeout net.Error
		if !errors.As(e, &timeout) || !timeout.Timeout() {
			t.Fatalf("bad bounded denial: %v", e)
		}
	}
	_ = existing.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 || s.EffectiveLimit != 0 {
		t.Fatalf("release manufactured FD budget: %+v", s)
	}
	g.sample(healthy, now.Add(2*time.Second))
	newClient := dial()
	defer newClient.Close()
	recovered, e := ln.Accept()
	if e != nil {
		t.Fatalf("fresh FD-only sensor failed recovery: %v", e)
	}
	_ = recovered.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 {
		t.Fatalf("physical permit leak: %+v", s)
	}
}
