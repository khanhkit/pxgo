package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// TestCapacityMixedStaggeredLifetimes complements 2560 simultaneously held
// tunnels with natural closing patterns. Real TCP tunnels survive short HTTP
// churn, then close at four distinct lifetime thresholds; the next wave may
// borrow any released GLOBAL slot regardless of soft class accounting.
func TestCapacityMixedStaggeredLifetimes(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_MIXED_BENCH") != "1" {
		t.Skip("explicit mixed-load benchmark")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("requires experimental AUTO")
	}
	seed, err := strconv.Atoi(os.Getenv("PXGO_CAPACITY_MIX_SEED"))
	if err != nil || seed < 0 || seed > 10000 {
		t.Fatalf("invalid seed: %v", err)
	}
	counts := []int{16 + seed%6, 24 + seed%8, 32 + seed%10, 40 + seed%12}
	targets := []time.Duration{250 * time.Millisecond, 950 * time.Millisecond, 2500 * time.Millisecond, 5500 * time.Millisecond}
	total := 0
	for _, v := range counts {
		total += v
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "mixed-ok") }))
	defer origin.Close()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var originWG sync.WaitGroup
	originWG.Add(1)
	go func() {
		defer originWG.Done()
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			originWG.Add(1)
			go func(c net.Conn) { defer originWG.Done(); defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	defer func() { _ = echo.Close(); originWG.Wait() }()
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.Idle = 40
	cfg.SockTimeout = 12
	px := startTestProxy(t, cfg)
	held := make([]net.Conn, total)
	defer func() {
		for _, c := range held {
			if c != nil {
				_ = c.Close()
			}
		}
	}()
	type opening struct {
		index  int
		socket net.Conn
		err    error
	}
	ready := make(chan opening, total)
	tokens := make(chan struct{}, 12)
	for i := 0; i < total; i++ {
		go func(index int) {
			tokens <- struct{}{}
			c, e := capacityMixedConnect(px.ListenAddr(), echo.Addr().String(), index)
			<-tokens
			ready <- opening{index, c, e}
		}(i)
	}
	var firstOpenError error
	for i := 0; i < total; i++ {
		r := <-ready
		if r.err != nil {
			if firstOpenError == nil {
				firstOpenError = r.err
			}
			continue
		}
		held[r.index] = r.socket
	}
	if firstOpenError != nil {
		t.Fatalf("initial live tunnel cohort incomplete: %v", firstOpenError)
	}
	opened := time.Now()
	closed := 0
	for phase := range counts {
		shortN := 48 + (seed*11+phase*7)%49
		shortLat := make(chan time.Duration, shortN)
		failures := make(chan error, shortN)
		gate := make(chan struct{}, 12)
		var wg sync.WaitGroup
		for i := 0; i < shortN; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				offered := time.Now()
				gate <- struct{}{}
				defer func() { <-gate }()
				c, e := net.DialTimeout("tcp", px.ListenAddr(), 5*time.Second)
				if e == nil {
					e = capacityMixedHTTP(c, bufio.NewReader(c), origin.URL, false)
					_ = c.Close()
				}
				if e != nil {
					failures <- e
					return
				}
				shortLat <- time.Since(offered)
			}()
		}
		wg.Wait()
		close(failures)
		close(shortLat)
		var firstShortError error
		for e := range failures {
			if firstShortError == nil {
				firstShortError = e
			}
		}
		latency := make([]time.Duration, 0, shortN)
		for v := range shortLat {
			latency = append(latency, v)
		}
		if firstShortError != nil || len(latency) != shortN {
			t.Fatalf("phase %d short demand incomplete: %d/%d first=%v", phase, len(latency), shortN, firstShortError)
		}
		if wait := time.Until(opened.Add(targets[phase])); wait > 0 {
			time.Sleep(wait)
		}
		for i := closed; i < closed+counts[phase]; i++ {
			_ = held[i].Close()
			held[i] = nil
		}
		closed += counts[phase]
		remaining := total - closed
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			snap := px.capacityDiagnosticSnapshot()
			if snap.ActiveDownstream == remaining && (snap.ClassPools == nil || snap.ClassPools.Total == remaining) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		snap := px.capacityDiagnosticSnapshot()
		if snap.ActiveDownstream != remaining {
			t.Fatalf("phase %d global permit mismatch, expected %d got %+v", phase, remaining, snap)
		}
		if os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") == "1" {
			if c := snap.ClassPools; c == nil || c.Total != remaining || c.Long != remaining || c.Short != 0 || c.Provisional != 0 {
				t.Fatalf("phase %d class split mismatch: %+v", phase, c)
			}
		} else if snap.ClassPools != nil {
			t.Fatalf("class tracker enabled without flag: %+v", snap.ClassPools)
		}
		// Last-mile data-plane liveness must survive unrelated short-wave churn and
		// earlier peers' disconnects. Probe every survivor before the next phase.
		for i := closed; i < total; i++ {
			if e := verifyCapacityHeldTunnel(held[i], i); e != nil {
				t.Fatalf("survivor %d failed after phase %d: %v", i, phase, e)
			}
		}
		_, p95, p99 := capacityLatencySummary(latency)
		t.Logf("LIFETIME_RESULT seed=%d phase=%d lifetime_target=%s short_attempted=%d short_completed=%d short_offered_p95=%s short_offered_p99=%s closed_long=%d remaining_long=%d global_active=%d class_debt=%d", seed, phase, targets[phase], shortN, len(latency), p95, p99, counts[phase], remaining, snap.ActiveDownstream, func() int {
			if snap.ClassPools == nil {
				return 0
			}
			return snap.ClassPools.LongDebt
		}())
	}
	if closed != total {
		t.Fatalf("incomplete phased close %d/%d", closed, total)
	}
	if s := px.capacityDiagnosticSnapshot(); s.ActiveDownstream != 0 || (s.ClassPools != nil && s.ClassPools.Total != 0) {
		t.Fatalf("final global/class cleanup leaked: %+v", s)
	}
	t.Logf("LIFETIME_SUMMARY seed=%d complete=true initial_held=%d all_closed=%d durations=4 class_mode=%s policy=%s", seed, total, closed, os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES"), os.Getenv("PXGO_RESEARCH_CAPACITY_POLICY"))
}
