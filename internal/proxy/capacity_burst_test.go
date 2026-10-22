package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// TestCapacityBurst2560 runs a tenfold oversubscription experiment against
// a 256-permit legacy baseline and experimental AUTO. It is opt-in because
// 2,560 simultaneous sockets plus upstream tunnel sockets can exceed CI
// container FD/memory limits. It records BOTH attempts and actual admissions;
// attempted dials are never confused with successful CONNECT handshakes.
func TestCapacityBurst2560(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_BURST_2560") != "1" {
		t.Skip("opt in to 2560 real concurrent CONNECT sockets")
	}
	mode := os.Getenv("PXGO_CAPACITY_BENCH_MODE")
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != string(modeLegacy) && mode != string(modeShadow) {
		t.Fatalf("unsupported burst mode %q", mode)
	}
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", mode)
	const attempts = 2560
	if runtime.GOOS == "linux" {
		s := readCapacitySample()
		if !s.available || s.freeFD < 3*attempts {
			t.Skipf("not enough Linux FD headroom for safe client+proxy+upstream load: %+v", s)
		}
	}
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			c, e := upstream.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(io.Discard, c) }(c)
		}
	}()
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.SockTimeout = 12
	cfg.Idle = 50
	cfg.Listen = "127.0.0.1"
	if mode == string(modeLegacy) || mode == string(modeShadow) {
		cfg.Workers = 1
		cfg.Threads = 256
	}
	px := startTestProxy(t, cfg)
	start := make(chan struct{})
	release := make(chan struct{})
	type outcome struct {
		success       bool
		latency       time.Duration
		dialError     bool
		beforeRelease bool
	}
	results := make(chan outcome, attempts)
	var admitted atomic.Int64
	var released atomic.Bool
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			<-start
			begun := time.Now()
			conn, e := net.DialTimeout("tcp", px.ListenAddr(), 3*time.Second)
			if e != nil {
				results <- outcome{dialError: true}
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(16 * time.Second))
			_, e = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.Addr(), upstream.Addr())
			if e == nil {
				var resp *http.Response
				resp, e = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
				if e == nil {
					if resp.StatusCode != http.StatusOK {
						e = fmt.Errorf("status %d", resp.StatusCode)
					}
					_ = resp.Body.Close()
				}
			}
			if e != nil {
				results <- outcome{}
				return
			}
			latency := time.Since(begun)
			beforeRelease := !released.Load()
			admitted.Add(1)
			<-release
			results <- outcome{success: true, latency: latency, beforeRelease: beforeRelease}
		}()
	}
	var memAt runtime.MemStats
	begun := time.Now()
	close(start) // all 2560 goroutines attempt a socket concurrently
	const phase = 13 * time.Second
	time.Sleep(phase)
	runtime.ReadMemStats(&memAt)
	peak := px.capacityDiagnosticSnapshot()
	successBeforeRelease := admitted.Load()
	tunnelsBeforeRelease := px.ActiveTunnels()
	goroutinesBeforeRelease := runtime.NumGoroutine()
	fdAt := readCapacitySample()
	released.Store(true)
	close(release)
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(21 * time.Second):
		t.Fatal("burst clients did not drain")
	}
	close(results)
	var successes, dialErrors, otherErrors int
	latencies := make([]time.Duration, 0, attempts)
	preReleaseLatencies := make([]time.Duration, 0, attempts)
	for result := range results {
		switch {
		case result.success:
			successes++
			latencies = append(latencies, result.latency)
			if result.beforeRelease {
				preReleaseLatencies = append(preReleaseLatencies, result.latency)
			}
		case result.dialError:
			dialErrors++
		default:
			otherErrors++
		}
	}
	if successes+dialErrors+otherErrors != attempts {
		t.Fatal("outcomes do not total 2560")
	}
	var p50, p95, p99 time.Duration
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 = latencies[len(latencies)/2]
		p95 = latencies[(95*len(latencies)+99)/100-1]
		p99 = latencies[(99*len(latencies)+99)/100-1]
	}
	var preReleaseP95 time.Duration
	if len(preReleaseLatencies) > 0 {
		sort.Slice(preReleaseLatencies, func(i, j int) bool { return preReleaseLatencies[i] < preReleaseLatencies[j] })
		preReleaseP95 = preReleaseLatencies[(95*len(preReleaseLatencies)+99)/100-1]
	}
	t.Logf("RESULT platform=%s mode=%s attempted=%d accepted_before_release=%d successful_total=%d dial_errors=%d other_errors=%d tunnels_at_peak=%d admission_active_peak=%d soft=%d hard=%d waits=%d timeouts=%d pre_release_p95=%s full_run_p50=%s full_run_p95=%s full_run_p99=%s goroutines_peak=%d shared_heap_alloc_at_peak=%d fd_free_at_peak=%d duration=%s", runtime.GOOS, mode, attempts, successBeforeRelease, successes, dialErrors, otherErrors, tunnelsBeforeRelease, peak.ActiveDownstream, peak.SoftWindow, peak.HardEnvelope, peak.AdmissionWaits, peak.AdmissionTimeouts, preReleaseP95, p50, p95, p99, goroutinesBeforeRelease, memAt.Alloc, fdAt.freeFD, time.Since(begun))
	if (mode == string(modeLegacy) || mode == string(modeShadow)) && peak.ActiveDownstream > 256 {
		t.Fatalf("fixed legacy budget violated: %+v", peak)
	}
	if mode == "auto" && runtime.GOOS == "linux" && peak.SensorAvailability == "fresh" && successBeforeRelease <= 256 {
		t.Fatalf("AUTO did not exceed legacy fixed budget despite healthy Linux headroom: %+v", peak)
	}
}
