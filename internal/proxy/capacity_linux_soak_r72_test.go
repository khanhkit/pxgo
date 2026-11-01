//go:build linux

package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/diagnostic"
)

// An opt-in, strictly physical TEN-minute Linux 2560 CONNECT soak with real short HTTP.
// Research-only Linux parity with the prior native Windows 10-minute test;
// this does NOT validate Windows, enterprise SSPI or pressured cgroup OOM recovery.
const (
	capacitySoakLongCount  = 2560
	capacitySoakCycles     = 30
	capacitySoakCycleDelay = 20 * time.Second
	capacitySoakShortCycle = 20
)

func TestR76LinuxTenMinuteSoak(t *testing.T) {
	if os.Getenv("PXGO_R76_LINUX_10MIN_SOAK") != "1" {
		t.Skip("opt-in Linux 10-minute 2560-held CONNECT soak")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" ||
		os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") != "1" {
		t.Fatal("soak requires explicit experimental AUTO + class ON")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "mixed-ok")
	}))
	defer origin.Close()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, e := echo.Accept()
			if e != nil {
				return
			}
			workers.Add(1)
			go func(c net.Conn) {
				defer workers.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	held := make([]net.Conn, capacitySoakLongCount)
	// Register cleanup BEFORE starting proxy: if proxy startup itself
	// t.Fatal's, the echo listener and accept/worker goroutines still exit.
	// Close held clients before echo listener, then join all real echo workers.
	defer func() {
		for i, conn := range held {
			if conn != nil {
				_ = conn.Close()
				held[i] = nil
			}
		}
		_ = echo.Close()
		workers.Wait()
	}()
	cfg := config.Default()
	cfg.Listen = "127.0.0.1"
	cfg.Idle = 55
	cfg.SockTimeout = 12
	cfg.ProxyReload = 0
	px := startTestProxy(t, cfg)
	type outcome struct {
		index int
		conn  net.Conn
		err   error
	}
	results := make(chan outcome, capacitySoakLongCount)
	launch := make(chan struct{}, 16)
	for i := range held {
		go func(index int) {
			launch <- struct{}{}
			conn, err := capacityMixedConnect(px.ListenAddr(), echo.Addr().String(), index)
			<-launch
			results <- outcome{index: index, conn: conn, err: err}
		}(i)
	}
	var firstError error
	admitted := 0
	// MUST collect all 2560 results BEFORE any failure, or orphan long
	// CONNECT goroutines may hang teardown and hide root cause.
	for range held {
		out := <-results
		if out.err != nil {
			if out.conn != nil {
				_ = out.conn.Close()
			}
			if firstError == nil {
				firstError = fmt.Errorf("CONNECT[%d] failed: %w", out.index, out.err)
			}
			continue
		}
		held[out.index] = out.conn
		admitted++
	}
	if firstError != nil || admitted != capacitySoakLongCount {
		t.Fatalf("long CONNECT plateau incomplete %d/%d first=%v", admitted, capacitySoakLongCount, firstError)
	}
	plateau := px.capacityDiagnosticSnapshot()
	if plateau.ActiveDownstream != capacitySoakLongCount ||
		plateau.ClassPools == nil || plateau.ClassPools.Total != capacitySoakLongCount ||
		plateau.ClassPools.Long != capacitySoakLongCount ||
		plateau.ClassPools.Short != 0 || plateau.ClassPools.Provisional != 0 {
		t.Fatalf("R72 2-minute initial plateau invalid %+v", plateau)
	}
	baseline, err := readCapacityIsolatedResources(os.Getpid())
	if err != nil {
		t.Fatalf("native private+handle resource baseline failed: %v", err)
	}
	var peakHandles uint64
	var peakPrivate uint64
	var totalShort int
	start := time.Now()
	for cycle := 1; cycle <= capacitySoakCycles; cycle++ {
		// Even if the scheduler is slow, monotonic elapsed time must reach
		// at least TEN minutes before emitting final successful marker.
		time.Sleep(capacitySoakCycleDelay)
		for i, conn := range held {
			if err := verifyCapacityHeldTunnel(conn, i); err != nil {
				t.Fatalf("live CONNECT[%d] payload failed during cycle=%d: %v", i, cycle, err)
			}
		}
		for n := 0; n < capacitySoakShortCycle; n++ {
			conn, err := net.DialTimeout("tcp", px.ListenAddr(), 5*time.Second)
			if err != nil {
				t.Fatalf("cycle=%d short[%d] dial: %v", cycle, n, err)
			}
			err = capacityMixedHTTP(conn, bufio.NewReader(conn), origin.URL, false)
			_ = conn.Close()
			if err != nil {
				t.Fatalf("cycle=%d short[%d] HTTP: %v", cycle, n, err)
			}
			totalShort++
		}
		// The HTTP client has completed response reading and Close(), but the
		// server goroutine may not have released that physical permit YET.
		// Do not mistake one transient active short for a leaked long slot:
		// require the exact physical/class plateau to settle within 3s.
		settleDeadline := time.Now().Add(3 * time.Second)
		settleStart := time.Now()
		var settled diagnostic.CapacitySnapshot
		var highestActive int
		for {
			settled = px.capacityDiagnosticSnapshot()
			if settled.ActiveDownstream > highestActive {
				highestActive = settled.ActiveDownstream
			}
			if settled.ClassPools == nil || settled.ClassPools.Long != capacitySoakLongCount ||
				settled.ActiveDownstream < capacitySoakLongCount ||
				settled.ActiveDownstream > capacitySoakLongCount+capacitySoakShortCycle {
				t.Fatalf("physical/class tunnel corruption at cycle=%d: %+v", cycle, settled)
			}
			if settled.ActiveDownstream == capacitySoakLongCount &&
				settled.ClassPools.Total == capacitySoakLongCount &&
				settled.ClassPools.Short == 0 && settled.ClassPools.Provisional == 0 {
				break
			}
			if !time.Now().Before(settleDeadline) {
				t.Fatalf("physical/class short permit failed to settle in 3s cycle=%d: %+v",
					cycle, settled)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if highestActive > capacitySoakLongCount {
			t.Logf("SOAK_SETTLE cycle=%d max_transient_active=%d settled_after=%s physical=2560",
				cycle, highestActive, time.Since(settleStart))
		}
		resources, newPrivate, newHandles := r72LinuxResourceSample(t, cycle, baseline, peakPrivate, peakHandles)
		peakPrivate, peakHandles = newPrivate, newHandles
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		t.Logf("R72_LINUX_SOAK_SAMPLE cycle=%d elapsed=%s held=2560 short_completed=%d test_process_rssanon=%d test_process_fd=%d heap_alloc=%d physical=2560",
			cycle, time.Since(start), totalShort, resources.PrivateBytes, resources.Handles, mem.Alloc)
	}
	elapsed := time.Since(start)
	if elapsed < capacitySoakCycles*capacitySoakCycleDelay || totalShort != capacitySoakCycles*capacitySoakShortCycle {
		t.Fatalf("R76 10-minute wall-clock / short-HTTP soak incomplete: elapsed=%s shorts=%d", elapsed, totalShort)
	}
	// Explicit CLOSE before final drain; the server remains alive for its
	// ordinary diagnostics and shutdown cleanup.
	for i, conn := range held {
		if conn == nil {
			t.Fatalf("held CONNECT[%d] unexpectedly nil after soak", i)
		}
		_ = conn.Close()
		held[i] = nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := px.capacityDiagnosticSnapshot()
		if s.ActiveDownstream == 0 && (s.ClassPools == nil || s.ClassPools.Total == 0) {
			t.Logf("R76_LINUX_SOAK_RESULT held=2560 heartbeat_cycles=%d short_completed=%d elapsed=%s baseline_rssanon=%d peak_rssanon=%d baseline_fd=%d peak_fd=%d physical_after=0",
				capacitySoakCycles, totalShort, elapsed, baseline.PrivateBytes, peakPrivate, baseline.Handles, peakHandles)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("R76 10-minute soak did not fully drain: %+v", px.capacityDiagnosticSnapshot())
}

// r72LinuxResourceSample keeps readback and research-only safety bounds
// separate from the on-wire session/permit proof. RssAnon is a metric, not
// Windows committed memory or a calibrated production capacity sensor.
func r72LinuxResourceSample(t *testing.T, cycle int, baseline capacityIsolatedResourceSample, peakPrivate, peakHandles uint64) (capacityIsolatedResourceSample, uint64, uint64) {
	t.Helper()
	resources, err := readCapacityIsolatedResources(os.Getpid())
	if err != nil {
		t.Fatalf("resource sample cycle=%d: %v", cycle, err)
	}
	peakPrivate = max(peakPrivate, resources.PrivateBytes)
	peakHandles = max(peakHandles, uint64(resources.Handles))
	const linuxLabRSSGuardrail = uint64(1 << 30)
	if resources.RSSBytes > linuxLabRSSGuardrail || uint64(resources.Handles) > uint64(baseline.Handles)+2048 {
		t.Fatalf("Linux lab guardrail exceeded: baseline=%+v current=%+v rss_guard=%d", baseline, resources, linuxLabRSSGuardrail)
	}
	return resources, peakPrivate, peakHandles
}
