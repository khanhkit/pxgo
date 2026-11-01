//go:build linux

package proxy

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestR80RealSocketAncestorPressureAndRecovery(t *testing.T) {
	root := t.TempDir()
	write := func(rel, name, v string) {
		t.Helper()
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("", "memory.max", "536870912")
	write("", "memory.current", "503316480")
	write("", "memory.high", "max")
	write("service", "memory.max", "2147483648")
	write("service", "memory.current", "10485760")
	write("service", "memory.high", "max")
	budget := func() capacitySample {
		t.Helper()
		result, err := readCapacityCgroupHierarchy(root, "service")
		if err != nil {
			t.Fatal(err)
		}
		return capacitySample{available: true, freeFD: 9000, totalFD: 10000, linuxMemoryConstrained: result.constrained, linuxMemoryFreeSlots: result.freeSlots, linuxMemoryPressure: result.pressured}
	}
	cfg := config.Default()
	cfg.MaxConnections = 2
	g := newCapacityGovernor(modeAuto, cfg)
	defer g.close()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newDynamicAdmissionListener(raw, g, 80*time.Millisecond)
	defer ln.Close()
	firstClient, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer firstClient.Close()
	first, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// Existing long-lived transport is not evicted by exhausted ancestor.
	now := time.Unix(86000, 0)
	low := budget()
	if !low.linuxMemoryConstrained || low.linuxMemoryFreeSlots != 0 {
		t.Fatalf("synthetic parent not exhausted: %+v", low)
	}
	g.sample(low, now)
	g.sample(low, now.Add(4*time.Second))
	if snap := g.snapshot(); snap.ActiveDownstream != 1 || snap.EffectiveLimit != 0 || snap.PressureReason != "cgroup-memory" {
		t.Fatalf("ancestor pressure ignored: %+v", snap)
	}
	// Real bidirectional data still works while new admissions are blocked.
	send := []byte("alive")
	recv := make([]byte, len(send))
	done := make(chan error, 1)
	go func() {
		frame := make([]byte, len(send))
		_, e := io.ReadFull(first, frame)
		if e == nil {
			_, e = first.Write(frame)
		}
		done <- e
	}()
	_ = firstClient.SetDeadline(time.Now().Add(time.Second))
	if _, err = firstClient.Write(send); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(firstClient, recv); err != nil || string(recv) != string(send) {
		t.Fatalf("existing transport failed %q: %v", recv, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	_ = firstClient.SetDeadline(time.Time{})
	_ = first.Close()
	if s := g.snapshot(); s.EffectiveLimit != 0 || s.ActiveDownstream != 0 {
		t.Fatalf("closed socket created stale ancestor-memory slot: %+v", s)
	}
	deniedClient, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer deniedClient.Close()
	if conn, err := ln.Accept(); err == nil {
		_ = conn.Close()
		t.Fatal("memory exhausted ancestor admitted new socket")
	} else {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("unexpected blocked admission error %v", err)
		}
	}
	// Fresh positive measurement is the only authority for recovery.
	write("", "memory.current", "134217728")
	healthy := budget()
	if healthy.linuxMemoryFreeSlots <= 0 || healthy.linuxMemoryPressure {
		t.Fatalf("fresh headroom absent %+v", healthy)
	}
	g.sample(healthy, now.Add(8*time.Second))
	g.sample(healthy, now.Add(9*time.Second))
	g.sample(healthy, now.Add(10*time.Second))
	recoveredClient, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredClient.Close()
	recovered, err := ln.Accept()
	if err != nil {
		t.Fatalf("fresh headroom did not reopen on real socket: %v", err)
	}
	_ = recovered.Close()
	if s := g.snapshot(); s.ActiveDownstream != 0 {
		t.Fatalf("permit leaked after recovery: %+v", s)
	}
}
