package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Real socket regression: the global cap is genuinely SATURATED by long
// CONNECT tunnels. A short HTTP request must wait for a physical permit; it
// can only progress after an existing long tunnel closes. This does NOT claim
// the advisory short/long class counters enforce separate admission queues.
func TestCapacityShortBorrowsReleasedLongPermit(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_MIXED_BENCH") != "1" {
		t.Skip("explicit opt-in real network stress")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("AUTO must be opted in")
	}
	const limit = 64
	const rounds = 4
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "mixed-ok") }))
	defer origin.Close()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) { defer wg.Done(); defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	defer func() { _ = echo.Close(); wg.Wait() }()
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.SockTimeout = 10
	cfg.Idle = 40
	cfg.MaxConnections = limit
	px := startTestProxy(t, cfg)
	held := make([]net.Conn, limit)
	defer func() {
		for _, c := range held {
			if c != nil {
				_ = c.Close()
			}
		}
	}()
	for i := range held {
		c, e := capacityMixedConnect(px.ListenAddr(), echo.Addr().String(), i)
		if e != nil {
			t.Fatalf("fill %d/%d: %v", i, limit, e)
		}
		held[i] = c
	}
	snap := px.capacityDiagnosticSnapshot()
	if snap.ActiveDownstream != limit || snap.EffectiveLimit != limit {
		t.Fatalf("physical cap not saturated: %+v", snap)
	}
	for round := 0; round < rounds; round++ {
		// Keep all live tunnels while attempting a new HTTP/1.1 socket. `Dial`
		// may complete at the TCP backlog, but the HTTP handler cannot run until
		// the capacity governor has acquired the returned physical permit.
		done := make(chan error, 1)
		start := time.Now()
		go func() {
			conn, e := net.DialTimeout("tcp", px.ListenAddr(), 3*time.Second)
			if e != nil {
				done <- e
				return
			}
			defer conn.Close()
			done <- capacityMixedHTTP(conn, bufio.NewReader(conn), origin.URL, false)
		}()
		select {
		case e := <-done:
			t.Fatalf("short request unexpectedly passed hard cap before long release round=%d err=%v", round, e)
		case <-time.After(150 * time.Millisecond):
		}
		before := px.capacityDiagnosticSnapshot()
		if before.ActiveDownstream != limit {
			t.Fatalf("physical cap shifted before release: %+v", before)
		}
		_ = held[round].Close()
		held[round] = nil
		select {
		case e := <-done:
			if e != nil {
				t.Fatalf("short request after release round=%d: %v", round, e)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("short starved after released long permit round=%d", round)
		}
		waitUntil := time.Now().Add(2 * time.Second)
		for time.Now().Before(waitUntil) && px.capacityDiagnosticSnapshot().ActiveDownstream != limit-1 {
			time.Sleep(5 * time.Millisecond)
		}
		if after := px.capacityDiagnosticSnapshot(); after.ActiveDownstream != limit-1 {
			t.Fatalf("short did not return physical permit: %+v", after)
		}
		replacement, e := capacityMixedConnect(px.ListenAddr(), echo.Addr().String(), limit+round)
		if e != nil {
			t.Fatalf("replace long after short round=%d: %v", round, e)
		}
		held[round] = replacement
		after := px.capacityDiagnosticSnapshot()
		if after.ActiveDownstream != limit {
			t.Fatalf("global physical permit not conserved: %+v", after)
		}
		if os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") == "1" {
			if c := after.ClassPools; c == nil || c.Long != limit || c.Short != 0 || c.Provisional != 0 || c.Total != limit {
				t.Fatalf("class permit ledger inconsistent: %+v", c)
			}
		}
		t.Logf("BORROW_RESULT round=%d manual_limit=%d active_before=%d short_blocked_for=150ms short_completed_after_release=true short_elapsed=%s long_replenished=true physical_active=%d", round, limit, before.ActiveDownstream, time.Since(start), after.ActiveDownstream)
	}
	for i, c := range held {
		_ = c.Close()
		held[i] = nil
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := px.capacityDiagnosticSnapshot()
		if s.ActiveDownstream == 0 && (s.ClassPools == nil || s.ClassPools.Total == 0) {
			t.Logf("BORROW_SUMMARY rounds=%d complete=true global_and_class_drained=true", rounds)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("global/class admission tokens leaked after saturated borrowing: %+v", px.capacityDiagnosticSnapshot())
	_ = fmt.Sprintf
}
