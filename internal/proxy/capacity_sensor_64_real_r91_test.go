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

func TestR91Real64HeldTunnelsSurviveSensorBlackout(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newDynamicAdmissionListener(raw, g, 35*time.Millisecond)
	defer listener.Close()
	sample := capacitySample{
		available: true, totalFD: 10000, freeFD: 9000,
		linuxMemoryConstrained: true, linuxMemoryFreeSlots: 128,
	}
	tick := time.Unix(460000, 0)
	g.sample(sample, tick)
	const held = 64
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
		client, e := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if e != nil {
			t.Fatalf("dial %d: %v", i, e)
		}
		server, e := listener.Accept()
		if e != nil {
			t.Fatalf("physical admit %d: %v", i, e)
		}
		clients = append(clients, client)
		servers = append(servers, server)
	}
	g.sample(capacitySample{reason: "statvfs unavailable"}, tick.Add(time.Second))
	if s := g.snapshot(); s.ActiveDownstream != held || s.EffectiveLimit != 0 {
		t.Fatalf("outage evicted held sockets or accepted unverified ones: %+v", s)
	}
	// Active bidirectional data transfer is still possible for all held sockets.
	for i := 0; i < held; i++ {
		packet := []byte{byte(i), 0xAB, 0xCD}
		_ = clients[i].SetDeadline(time.Now().Add(time.Second))
		_ = servers[i].SetDeadline(time.Now().Add(time.Second))
		if _, e := clients[i].Write(packet); e != nil {
			t.Fatalf("client%d write: %v", i, e)
		}
		b := make([]byte, len(packet))
		if _, e := io.ReadFull(servers[i], b); e != nil {
			t.Fatalf("server%d read: %v", i, e)
		}
		if _, e := servers[i].Write(b); e != nil {
			t.Fatalf("server%d write: %v", i, e)
		}
		if _, e := io.ReadFull(clients[i], b); e != nil || string(b) != string(packet) {
			t.Fatalf("client%d heartbeat: %x %v", i, b, e)
		}
	}
	for i := 0; i < 32; i++ {
		_ = servers[i].Close()
	}
	if s := g.snapshot(); s.ActiveDownstream != 32 || s.EffectiveLimit != 0 {
		t.Fatalf("close during blackout manufactured new permits: %+v", s)
	}
	extra, e := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer extra.Close()
	if c, e := listener.Accept(); e == nil {
		_ = c.Close()
		t.Fatal("unknown sensor allowed extra TCP")
	} else {
		var timeout net.Error
		if !errors.As(e, &timeout) || !timeout.Timeout() {
			t.Fatalf("unknown sensor error %v", e)
		}
	}
	g.sample(sample, tick.Add(4*time.Second))
	recoveredClient, e := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer recoveredClient.Close()
	recovered, e := listener.Accept()
	if e != nil {
		t.Fatalf("fresh sensor failed recovery: %v", e)
	}
	_ = recovered.Close()
	for i := 32; i < held; i++ {
		_ = servers[i].Close()
	}
	if s := g.snapshot(); s.ActiveDownstream != 0 {
		t.Fatalf("physical permits not drained: %+v", s)
	}
}
