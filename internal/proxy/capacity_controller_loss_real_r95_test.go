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

func TestR95RealTCPFiniteControllerLossAndReappearance(t *testing.T) {
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
	finite := capacitySample{available: true, freeFD: 10000, totalFD: 11000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 3}
	now := time.Unix(522000, 0)
	g.sample(finite, now)
	originalClient := dial()
	defer originalClient.Close()
	originalServer, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer originalServer.Close()
	g.sample(capacitySample{available: true, freeFD: 10000, totalFD: 11000}, now.Add(time.Second))
	if s := g.snapshot(); s.ActiveDownstream != 1 || s.EffectiveLimit != 0 {
		t.Fatalf("controller downgraded while old socket held: %+v", s)
	}
	_ = originalClient.SetDeadline(time.Now().Add(time.Second))
	_ = originalServer.SetDeadline(time.Now().Add(time.Second))
	msg := []byte("live")
	if _, err = originalClient.Write(msg); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len(msg))
	if _, err = io.ReadFull(originalServer, buffer); err != nil || string(buffer) != string(msg) {
		t.Fatalf("existing physical socket evicted: %q %v", buffer, err)
	}
	blockedClient := dial()
	defer blockedClient.Close()
	if c, e := ln.Accept(); e == nil {
		_ = c.Close()
		t.Fatal("absent finite controller incorrectly admitted TCP")
	} else {
		var timeout net.Error
		if !errors.As(e, &timeout) || !timeout.Timeout() {
			t.Fatalf("expected bounded admission denial, got %v", e)
		}
	}
	_ = originalServer.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 || s.EffectiveLimit != 0 {
		t.Fatalf("released long socket minted FD-only headroom: %+v", s)
	}
	g.sample(finite, now.Add(2*time.Second))
	recoveredClient := dial()
	defer recoveredClient.Close()
	recoveredServer, err := ln.Accept()
	if err != nil {
		t.Fatalf("finite valid sensor failed recovery: %v", err)
	}
	_ = recoveredServer.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 {
		t.Fatalf("recovered physical permit leaked: %+v", s)
	}
}
