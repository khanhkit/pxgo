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

// Contrary to steady held-CONTENT workloads, this gate launches long CONNECT
// and transient HTTP requests at the SAME instant, forcing shared physical
// governor admission during both classes' initial demand ramp.
func TestCapacityMixedCompetingAdmission(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_MIXED_BENCH") != "1" {
		t.Skip("explicit mixed-load benchmark only")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("requires experimental auto")
	}
	seed, _ := strconv.Atoi(os.Getenv("PXGO_CAPACITY_MIX_SEED"))
	longN := 256 + (seed%4)*64
	shortN := 256 + ((seed*3)%4)*64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "mixed-ok") }))
	defer origin.Close()
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var serverWG sync.WaitGroup
	serverWG.Add(1)
	go func() {
		defer serverWG.Done()
		for {
			c, e := echoListener.Accept()
			if e != nil {
				return
			}
			serverWG.Add(1)
			go func() { defer serverWG.Done(); defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	defer func() { _ = echoListener.Close(); serverWG.Wait() }()
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.Idle = 50
	cfg.SockTimeout = 12
	px := startTestProxy(t, cfg)
	type output struct {
		long    bool
		socket  net.Conn
		latency time.Duration
		err     error
	}
	results := make(chan output, longN+shortN)
	longTokens := make(chan struct{}, 16)
	shortTokens := make(chan struct{}, 16)
	goSignal := make(chan struct{})
	for i := 0; i < longN; i++ {
		go func(index int) {
			<-goSignal
			begin := time.Now()
			longTokens <- struct{}{}
			c, e := capacityMixedConnect(px.ListenAddr(), echoListener.Addr().String(), index)
			<-longTokens
			results <- output{long: true, socket: c, latency: time.Since(begin), err: e}
		}(i)
	}
	for i := 0; i < shortN; i++ {
		go func() {
			<-goSignal
			begin := time.Now()
			shortTokens <- struct{}{}
			c, e := net.DialTimeout("tcp", px.ListenAddr(), 5*time.Second)
			if e == nil {
				e = capacityMixedHTTP(c, bufio.NewReader(c), origin.URL, false)
				_ = c.Close()
			}
			<-shortTokens
			results <- output{latency: time.Since(begin), err: e}
		}()
	}
	started := time.Now()
	close(goSignal)
	held := make([]net.Conn, 0, longN)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	shorts, firstError := 0, error(nil)
	longLatencies := make([]time.Duration, 0, longN)
	shortLatencies := make([]time.Duration, 0, shortN)
	// Drain EVERY result even if some requests fail, so no orphan tunnel survives
	// test teardown and a failed benchmark cannot obscure its original error.
	for i := 0; i < longN+shortN; i++ {
		result := <-results
		if result.err != nil {
			if firstError == nil {
				firstError = result.err
			}
			continue
		}
		if result.long {
			held = append(held, result.socket)
			longLatencies = append(longLatencies, result.latency)
		} else {
			shorts++
			shortLatencies = append(shortLatencies, result.latency)
		}
	}
	if firstError != nil || len(held) != longN || shorts != shortN {
		t.Fatalf("competing admission failed: long=%d/%d short=%d/%d native_errno=%d first=%v",
			len(held), longN, shorts, shortN, capacityNativeSocketErrno(firstError), firstError)
	}
	for i, c := range held {
		if e := verifyCapacityHeldTunnel(c, i); e != nil {
			t.Fatalf("post-competition CONNECT payload lost: %v", e)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for px.capacityDiagnosticSnapshot().ActiveDownstream != longN && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	snap := px.capacityDiagnosticSnapshot()
	if snap.ActiveDownstream != longN {
		t.Fatalf("short class depleted global permits unexpectedly: %+v", snap)
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") == "1" {
		if c := snap.ClassPools; c == nil || c.Long != longN || c.Short != 0 || c.Provisional != 0 || c.Total != longN {
			t.Fatalf("mixed class conservation failure: %+v", c)
		}
	}
	_, longP95, _ := capacityLatencySummary(longLatencies)
	_, shortP95, shortP99 := capacityLatencySummary(shortLatencies)
	t.Logf("MIXED_COMPETE_RESULT seed=%d long_attempted=%d long_admitted=%d short_attempted=%d short_completed=%d long_offered_p95=%s short_offered_p95=%s short_offered_p99=%s physical_active=%d elapsed=%s", seed, longN, len(held), shortN, shorts, longP95, shortP95, shortP99, snap.ActiveDownstream, time.Since(started))
	for _, c := range held {
		_ = c.Close()
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := px.capacityDiagnosticSnapshot()
		if s.ActiveDownstream == 0 && (s.ClassPools == nil || s.ClassPools.Total == 0) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("competing admission drain leak: %+v", px.capacityDiagnosticSnapshot())
}
