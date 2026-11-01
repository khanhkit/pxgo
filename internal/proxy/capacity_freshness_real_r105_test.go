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

func TestR105RealTCPStalledSamplerPreservesExistingTrafficAndRecovers(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newDynamicAdmissionListener(raw, g, 50*time.Millisecond)
	defer ln.Close()
	sample := capacitySample{available: true, totalFD: 10000, freeFD: 9000}
	now := time.Unix(700000, 0) // synthetic timestamp intentionally old
	g.sample(sample, now)
	oldClient, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer oldClient.Close()
	oldServer, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer oldServer.Close()
	g.mu.Lock()
	g.lastVerifiedReceipt = time.Now().Add(-2 * capacitySensorMaxAge)
	g.mu.Unlock()
	if s := g.snapshot(); s.ActiveDownstream != 1 || s.EffectiveLimit != 0 {
		t.Fatalf("real stalled sample must close NEW admission without eviction: %+v", s)
	}
	_ = oldClient.SetDeadline(time.Now().Add(time.Second))
	_ = oldServer.SetDeadline(time.Now().Add(time.Second))
	msg := []byte("alive")
	if _, err = oldClient.Write(msg); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, len(msg))
	if _, err = io.ReadFull(oldServer, b); err != nil || string(b) != string(msg) {
		t.Fatalf("existing physical connection failed after sensor expiry: %q %v", b, err)
	}
	deniedClient, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer deniedClient.Close()
	if c, e := ln.Accept(); e == nil {
		_ = c.Close()
		t.Fatal("stale sample accepted unverified new TCP")
	} else {
		var timeout net.Error
		if !errors.As(e, &timeout) || !timeout.Timeout() {
			t.Fatalf("expected bounded timeout: %v", e)
		}
	}
	_ = oldServer.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 || s.EffectiveLimit != 0 {
		t.Fatalf("closing old socket manufactured stale headroom: %+v", s)
	}
	// New sample has an arbitrarily old synthetic event time, yet was
	// received NOW. Monotonic receipt must allow safe recovery.
	g.sample(sample, now.Add(time.Second))
	restoredClient, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredClient.Close()
	restored, err := ln.Accept()
	if err != nil {
		t.Fatalf("valid receipt didn't reopen gate: %v", err)
	}
	_ = restored.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 {
		t.Fatalf("permit leak: %+v", s)
	}
}
