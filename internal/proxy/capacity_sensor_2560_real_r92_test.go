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

// Experimental/native Linux integration: keep 2,560 real TCP sockets alive
// while the *simulated* FD/cgroup sensor fails. This is not a kernel OOM test
// and must not be interpreted as a native Windows/SSPI release gate.
func TestR92Native2560HeldUnderSensorOutageAndRecovery(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	sample := capacitySample{available: true, totalFD: 50000, freeFD: 45000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 8000}
	now := time.Unix(470000, 0)
	g.sample(sample, now)
	g.mu.Lock()
	g.soft = 4096 // test-specific demand saturation, not changed production policy
	g.recomputeLocked()
	g.mu.Unlock()
	raw, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ln := newDynamicAdmissionListener(raw, g, 40*time.Millisecond)
	defer ln.Close()
	const held = 2560
	clients := make([]net.Conn, 0, held)
	servers := make([]net.Conn, 0, held)
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
		for _, c := range servers {
			_ = c.Close()
		}
	}()
	for i := 0; i < held; i++ {
		client, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		server, err := ln.Accept()
		if err != nil {
			t.Fatalf("server %d: %v", i, err)
		}
		clients = append(clients, client)
		servers = append(servers, server)
	}
	if s := g.snapshot(); s.ActiveDownstream != held {
		t.Fatalf("held plateau not reached: %+v", s)
	}
	g.sample(capacitySample{reason: "RLIMIT/FD sensor temporarily unavailable"}, now.Add(time.Second))
	if s := g.snapshot(); s.ActiveDownstream != held || s.EffectiveLimit != 0 {
		t.Fatalf("sensor blackout evicted traffic or kept stale intake: %+v", s)
	}
	// Echo sampled real sockets after the memory/FD sensor is gone. The
	// physical gate must never evict already accepted useful connections.
	for i := 0; i < held; i += 40 {
		msg := []byte{byte(i), byte(i >> 8), 0x5A, 0x0F}
		_ = clients[i].SetDeadline(time.Now().Add(time.Second))
		_ = servers[i].SetDeadline(time.Now().Add(time.Second))
		if _, err := clients[i].Write(msg); err != nil {
			t.Fatalf("client%d write %v", i, err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(servers[i], buf); err != nil {
			t.Fatalf("server%d read %v", i, err)
		}
		if _, err := servers[i].Write(buf); err != nil {
			t.Fatalf("server%d write %v", i, err)
		}
		if _, err := io.ReadFull(clients[i], buf); err != nil || string(buf) != string(msg) {
			t.Fatalf("client%d echo %x %v", i, buf, err)
		}
	}
	deniedClient, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer deniedClient.Close()
	if c, err := ln.Accept(); err == nil {
		_ = c.Close()
		t.Fatal("blackout allowed physical socket #2561 without a valid sensor")
	} else {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("unexpected blackout denial: %v", err)
		}
	}
	for i := 0; i < 256; i++ {
		_ = servers[i].Close()
	}
	if s := g.snapshot(); s.ActiveDownstream != held-256 || s.EffectiveLimit != 0 {
		t.Fatalf("old closures fabricated headroom during blackout: %+v", s)
	}
	g.sample(sample, now.Add(2*time.Second))
	newClient, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer newClient.Close()
	newServer, err := ln.Accept()
	if err != nil {
		t.Fatalf("fresh finite sensor did not resume intake: %v", err)
	}
	_ = newServer.Close()
	for i := 256; i < held; i++ {
		_ = servers[i].Close()
	}
	if s := g.snapshot(); s.ActiveDownstream != 0 {
		t.Fatalf("physical permits leaked after native 2560 blackout: %+v", s)
	}
}
