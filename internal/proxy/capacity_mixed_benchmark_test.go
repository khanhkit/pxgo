package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Real network workload: admitted persistent CONNECTs coexist with short HTTP
// churn, HTTP/1.1 keepalive, and repeated HTTP->long promotions. Tests run only
// on demand, never in the lightweight default suite.
func TestCapacityMixedTrafficBench(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_MIXED_BENCH") != "1" {
		t.Skip("enable mixed network benchmark explicitly")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("mixed research needs explicit experimental AUTO mode")
	}
	profiles := []struct {
		name                               string
		tunnels, keepAlive, promote, churn int
	}{
		{"short_heavy", 192, 64, 16, 600},
		{"balanced", 512, 128, 32, 600},
		{"long_heavy", 1024, 64, 64, 250},
		{"churn", 384, 48, 48, 1200},
	}
	seed := 0
	if raw := os.Getenv("PXGO_CAPACITY_MIX_SEED"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 10000 {
			t.Fatalf("invalid mixed seed %q", raw)
		}
		seed = parsed
	}
	for _, p := range profiles {
		if only := os.Getenv("PXGO_CAPACITY_MIX_PROFILE"); only != "" && only != p.name {
			continue
		}
		// Reproducible +/-20% demand intensities across distinct benchmark runs.
		// Keep categories, protocols, and concurrency settings fixed so seed
		// variations affect offered load rather than dropping coverage.
		factor := 80 + (seed*13)%41
		p.tunnels = max(32, p.tunnels*factor/100)
		p.churn = max(32, p.churn*factor/100)
		p.promote = max(8, p.promote*(80+(seed*19+5)%41)/100)
		p.keepAlive = max(16, p.keepAlive*(80+(seed*7+23)%41)/100)
		t.Run(p.name, func(t *testing.T) { runCapacityMixedProfile(t, p.name, p.tunnels, p.keepAlive, p.promote, p.churn) })
	}
}

// Native syscall errno is evidence only; do not downgrade or waive an I/O failure.
func capacityNativeSocketErrno(err error) int {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return int(errno)
	}
	return -1
}

func TestCapacityNativeSocketErrnoExtraction(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil", want: -1},
		{name: "wrapped WSAENOBUFS", err: fmt.Errorf("wrapped dial: %w", syscall.Errno(10055)), want: 10055},
		{name: "net.OpError WSAEADDRNOTAVAIL", err: &net.OpError{
			// Op/Net labels are irrelevant to errors.As: test the wrapped
			// native WinSock errno without increasing unrelated goconst counts.
			Err: &os.SyscallError{Syscall: "bind", Err: syscall.Errno(10049)},
		}, want: 10049},
		{name: "plain application failure", err: errors.New("application failure"), want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capacityNativeSocketErrno(tc.err); got != tc.want {
				t.Fatalf("native errno got=%d expected=%d for error=%v", got, tc.want, tc.err)
			}
		})
	}
}

func capacityMixedHTTP(conn net.Conn, reader *bufio.Reader, target string, keep bool) error {
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	connection := "close"
	if keep {
		connection = "keep-alive"
	}
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: example.test\r\nConnection: %s\r\n\r\n", target, connection); err != nil {
		return err
	}
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return err
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 64))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(payload, []byte("mixed-ok")) {
		return fmt.Errorf("HTTP response mismatch: status=%d payload=%q", resp.StatusCode, payload)
	}
	return nil
}

func capacityMixedConnect(proxy, upstream string, index int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxy, 4*time.Second)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if err = conn.SetDeadline(time.Now().Add(12 * time.Second)); err != nil {
		return nil, err
	}
	if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstream, upstream); err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CONNECT status=%s", response.Status)
	}
	_ = response.Body.Close()
	probe := []byte(fmt.Sprintf("held-mixed-%08d", index))
	if _, err = conn.Write(probe); err != nil {
		return nil, err
	}
	echo := make([]byte, len(probe))
	if _, err = io.ReadFull(conn, echo); err != nil {
		return nil, err
	}
	if !bytes.Equal(probe, echo) {
		return nil, fmt.Errorf("CONNECT echo mismatch")
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}

func runCapacityMixedProfile(t *testing.T, name string, tunnels, keepAlive, promote, churn int) {
	t.Helper()
	httpOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "mixed-ok") }))
	defer httpOrigin.Close()
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var upstreamWG sync.WaitGroup
	upstreamWG.Add(1)
	go func() {
		defer upstreamWG.Done()
		for {
			c, e := echoListener.Accept()
			if e != nil {
				return
			}
			upstreamWG.Add(1)
			go func(c net.Conn) { defer upstreamWG.Done(); defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	defer func() { _ = echoListener.Close(); upstreamWG.Wait() }()
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.Idle = 55
	cfg.SockTimeout = 12
	cfg.Listen = "127.0.0.1"
	px := startTestProxy(t, cfg)
	held := make([]net.Conn, 0, tunnels+keepAlive+promote)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	start := time.Now()
	type connected struct {
		conn net.Conn
		err  error
	}
	deliveries := make(chan connected, tunnels)
	gate := make(chan struct{}, 32)
	for i := 0; i < tunnels; i++ {
		go func(index int) {
			gate <- struct{}{}
			c, e := capacityMixedConnect(px.ListenAddr(), echoListener.Addr().String(), index)
			<-gate
			deliveries <- connected{c, e}
		}(i)
	}
	var firstConnectError error
	for i := 0; i < tunnels; i++ {
		res := <-deliveries
		if res.err != nil {
			if firstConnectError == nil {
				firstConnectError = res.err
			}
			continue
		}
		held = append(held, res.conn)
	}
	// Drain the entire asynchronous cohort before failing, otherwise a
	// canceled test leaves unowned successful sockets blocking teardown.
	if firstConnectError != nil {
		t.Fatalf("CONNECT setup failures (%d/%d admitted): %v", len(held), tunnels, firstConnectError)
	}
	for i := 0; i < keepAlive+promote; i++ {
		c, e := net.DialTimeout("tcp", px.ListenAddr(), 4*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		reader := bufio.NewReader(c)
		rounds := 1
		if i >= keepAlive {
			rounds = classRequestThreshold
		}
		for n := 0; n < rounds; n++ {
			if e = capacityMixedHTTP(c, reader, httpOrigin.URL, true); e != nil {
				_ = c.Close()
				t.Fatalf("keepalive %d round %d: %v", i, n, e)
			}
		}
		_ = c.SetDeadline(time.Time{})
		held = append(held, c)
	}
	// Short requests must make forward progress while existing CONNECT tunnels
	// and keepalive sockets remain held. Use bounded 32-way dial concurrency on
	// Windows to avoid conflating pending TCP SYN backlog with governor fairness.
	latencies := make(chan time.Duration, churn)
	offeredLatencies := make(chan time.Duration, churn)
	problems := make(chan error, churn)
	tokens := make(chan struct{}, 32)
	var wg sync.WaitGroup
	workBegin := time.Now()
	for i := 0; i < churn; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			offeredAt := time.Now()
			tokens <- struct{}{}
			defer func() { <-tokens }()
			begin := time.Now()
			c, e := net.DialTimeout("tcp", px.ListenAddr(), 5*time.Second)
			if e != nil {
				problems <- e
				return
			}
			reader := bufio.NewReader(c)
			e = capacityMixedHTTP(c, reader, httpOrigin.URL, false)
			_ = c.Close()
			if e != nil {
				problems <- e
				return
			}
			latencies <- time.Since(begin)
			offeredLatencies <- time.Since(offeredAt)
		}()
	}
	wg.Wait()
	close(problems)
	close(latencies)
	close(offeredLatencies)
	for e := range problems {
		t.Errorf("short request failed: native_errno=%d err=%v", capacityNativeSocketErrno(e), e)
	}
	elapsedShort := time.Since(workBegin)
	values := make([]time.Duration, 0, churn)
	for v := range latencies {
		values = append(values, v)
	}
	if len(values) != churn {
		t.Fatalf("short-throughput starvation: completed=%d attempted=%d", len(values), churn)
	}
	offered := make([]time.Duration, 0, churn)
	for v := range offeredLatencies {
		offered = append(offered, v)
	}
	if len(offered) != churn {
		t.Fatalf("offered-latency missing samples: got=%d expected=%d", len(offered), churn)
	}
	// Actual held sockets retain a live DATA path after short-load churn.
	for i := 0; i < tunnels; i++ {
		if e := verifyCapacityHeldTunnel(held[i], i); e != nil {
			t.Fatalf("long tunnel %d died while serving shorts: %v", i, e)
		}
	}
	wantHeld := len(held)
	deadline := time.Now().Add(4 * time.Second)
	var snap int
	for time.Now().Before(deadline) {
		snap = px.capacityDiagnosticSnapshot().ActiveDownstream
		if snap == wantHeld {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap != wantHeld {
		t.Fatalf("global physical permit leak/undercount: active=%d expected=%d", snap, wantHeld)
	}
	c := px.capacityDiagnosticSnapshot().ClassPools
	if os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") == "1" {
		if c == nil || c.Total != wantHeld || c.Long != tunnels+promote || c.Short != keepAlive || c.Provisional != 0 || c.Promoted != uint64(promote) {
			t.Fatalf("class accounting migration: got=%+v tunnels=%d keepalive=%d promoted=%d", c, tunnels, keepAlive, promote)
		}
	} else if c != nil {
		t.Fatalf("class policy off must not track: %+v", c)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	p50, p95, p99 := capacityLatencySummary(values)
	_, offeredP95, offeredP99 := capacityLatencySummary(offered)
	debt, events := 0, uint64(0)
	if c != nil {
		debt = c.LongDebt
		events = c.LongOverflow
	}
	t.Logf("MIXED_RESULT profile=%s seed=%s held_long_connect=%d held_short=%d promoted_short_to_long=%d short_attempted=%d short_completed=%d short_p50=%s short_p95=%s short_p99=%s short_offered_p95=%s short_offered_p99=%s short_rps=%.2f class_current_debt=%d class_overflow_events=%d physical_active=%d heap_shared_bytes=%d goroutines_shared=%d duration=%s policy=%s classes=%s", name, os.Getenv("PXGO_CAPACITY_MIX_SEED"), tunnels, keepAlive, promote, churn, len(values), p50, p95, p99, offeredP95, offeredP99, float64(churn)/elapsedShort.Seconds(), debt, events, snap, mem.Alloc, runtime.NumGoroutine(), time.Since(start), os.Getenv("PXGO_RESEARCH_CAPACITY_POLICY"), os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES"))
	for _, c := range held {
		_ = c.Close()
	}
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		s := px.capacityDiagnosticSnapshot()
		if s.ActiveDownstream == 0 && (s.ClassPools == nil || s.ClassPools.Total == 0) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("drain leaked global or class permits: %+v", px.capacityDiagnosticSnapshot())
}
