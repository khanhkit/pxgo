package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/diagnostic"
)

// The probe process runs ONLY PXGO and its admission goroutines, while this
// parent runs the HTTP/CONNECT load generator and upstream echo/HTTP origins.
// The child is the same immutable test binary, never a public listening proxy.
func TestCapacityIsolatedProxyChild(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_ISOLATED_CHILD") != "1" {
		t.Skip("private benchmark child")
	}
	cfg := config.Default()
	cfg.Listen = "127.0.0.1"
	cfg.Idle = 50
	cfg.ProxyReload = 0
	cfg.SockTimeout = 20
	px := startTestProxy(t, cfg)
	fmt.Printf("ISOLATED_READY addr=%s pid=%d\n", px.ListenAddr(), os.Getpid())
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "SNAP":
			raw, err := json.Marshal(px.capacityDiagnosticSnapshot())
			if err != nil {
				t.Fatal(err)
			}
			fmt.Printf("ISOLATED_SNAPSHOT %s\n", raw)
		case "QUIT":
			return
		default:
			t.Fatal("unrecognized isolation control command")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

type capacityIsolatedProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	output *bufio.Reader
	cancel context.CancelFunc
}

func (p *capacityIsolatedProcess) readPrefix(prefix string) (string, error) {
	for i := 0; i < 1000; i++ {
		line, err := p.output.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), nil
		}
	}
	return "", errors.New("isolated proxy produced excessive output without handshake")
}

func startCapacityIsolatedProcess(t *testing.T) (*capacityIsolatedProcess, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapacityIsolatedProxyChild$", "-test.v", "-test.timeout=85s")
	cmd.Env = append(os.Environ(), "PXGO_CAPACITY_ISOLATED_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	p := &capacityIsolatedProcess{cmd: cmd, stdin: stdin, output: bufio.NewReader(stdout), cancel: cancel}
	t.Cleanup(func() {
		_, _ = io.WriteString(stdin, "QUIT\n")
		_ = stdin.Close()
		// Normal child exit is required: do not silently accept a killed proxy.
		if err := cmd.Wait(); err != nil {
			t.Errorf("isolated PXGO child failed: %v, stderr=%q", err, stderr.String())
		}
		cancel()
	})
	ready, err := p.readPrefix("ISOLATED_READY ")
	if err != nil {
		t.Fatalf("isolated proxy failed readiness: %v stderr=%q", err, stderr.String())
	}
	fields := strings.Fields(ready)
	for _, field := range fields {
		if strings.HasPrefix(field, "addr=") {
			return p, strings.TrimPrefix(field, "addr=")
		}
	}
	t.Fatalf("isolated proxy did not expose listener: %q", ready)
	return nil, ""
}

func (p *capacityIsolatedProcess) snapshot() (diagnostic.CapacitySnapshot, error) {
	var snap diagnostic.CapacitySnapshot
	if _, err := io.WriteString(p.stdin, "SNAP\n"); err != nil {
		return snap, err
	}
	line, err := p.readPrefix("ISOLATED_SNAPSHOT ")
	if err != nil {
		return snap, err
	}
	err = json.Unmarshal([]byte(line), &snap)
	return snap, err
}

//nolint:gocyclo // test-only orchestration retains explicit per-resource error gates.
func TestCapacityIsolatedProcessResourceBench(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_ISOLATED_BENCH") != "1" {
		t.Skip("opt-in separated-process memory and handle benchmark")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("isolated benchmark requires AUTO")
	}
	seed, err := strconv.Atoi(os.Getenv("PXGO_CAPACITY_ISOLATED_SEED"))
	if err != nil || seed < 0 || seed > 16 {
		t.Fatalf("invalid benchmark seed %q", os.Getenv("PXGO_CAPACITY_ISOLATED_SEED"))
	}
	child, proxy := startCapacityIsolatedProcess(t)
	baseline, err := readCapacityIsolatedResources(child.cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
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
	heldN := 256 + (seed%3)*64
	shortN := 400 + (seed%3)*100
	held := make([]net.Conn, heldN)
	defer func() {
		for _, c := range held {
			if c != nil {
				_ = c.Close()
			}
		}
	}()
	type connResult struct {
		idx  int
		conn net.Conn
		err  error
	}
	opened := make(chan connResult, heldN)
	tokens := make(chan struct{}, 16)
	for i := range held {
		go func(i int) {
			tokens <- struct{}{}
			c, e := capacityMixedConnect(proxy, echo.Addr().String(), i)
			<-tokens
			opened <- connResult{i, c, e}
		}(i)
	}
	var firstErr error
	for i := 0; i < heldN; i++ {
		res := <-opened
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		held[res.idx] = res.conn
	}
	if firstErr != nil {
		t.Fatalf("isolated CONNECT cohort failed: %v", firstErr)
	}
	heldMetrics, err := readCapacityIsolatedResources(child.cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	var load sync.WaitGroup
	shortErrors := make(chan error, shortN)
	shortLatency := make(chan time.Duration, shortN)
	shortTokens := make(chan struct{}, 16)
	started := time.Now()
	for i := 0; i < shortN; i++ {
		load.Add(1)
		go func() {
			defer load.Done()
			offered := time.Now()
			shortTokens <- struct{}{}
			defer func() { <-shortTokens }()
			c, e := net.DialTimeout("tcp", proxy, 5*time.Second)
			if e != nil {
				shortErrors <- e
				return
			}
			e = capacityMixedHTTP(c, bufio.NewReader(c), origin.URL, false)
			_ = c.Close()
			if e != nil {
				shortErrors <- e
				return
			}
			shortLatency <- time.Since(offered)
		}()
	}
	// Sample ONLY the child PID, not the Go test parent or upstream processes.
	peak := heldMetrics
	done := make(chan struct{})
	go func() { load.Wait(); close(done) }()
	monitor := time.NewTicker(100 * time.Millisecond)
	defer monitor.Stop()
	for running := true; running; {
		select {
		case <-done:
			running = false
		case <-monitor.C:
			sampled, e := readCapacityIsolatedResources(child.cmd.Process.Pid)
			if e != nil {
				t.Fatal(e)
			}
			if sampled.PrivateBytes > peak.PrivateBytes {
				peak.PrivateBytes = sampled.PrivateBytes
			}
			if sampled.RSSBytes > peak.RSSBytes {
				peak.RSSBytes = sampled.RSSBytes
			}
			if sampled.Handles > peak.Handles {
				peak.Handles = sampled.Handles
			}
			if sampled.CPUNanos > peak.CPUNanos {
				peak.CPUNanos = sampled.CPUNanos
			}
		}
	}
	elapsed := time.Since(started)
	finalResources, e := readCapacityIsolatedResources(child.cmd.Process.Pid)
	if e != nil {
		t.Fatal(e)
	}
	if finalResources.PrivateBytes > peak.PrivateBytes {
		peak.PrivateBytes = finalResources.PrivateBytes
	}
	if finalResources.RSSBytes > peak.RSSBytes {
		peak.RSSBytes = finalResources.RSSBytes
	}
	if finalResources.Handles > peak.Handles {
		peak.Handles = finalResources.Handles
	}
	if finalResources.CPUNanos > peak.CPUNanos {
		peak.CPUNanos = finalResources.CPUNanos
	}
	close(shortErrors)
	close(shortLatency)
	latencies := make([]time.Duration, 0, shortN)
	for sample := range shortLatency {
		latencies = append(latencies, sample)
	}
	if len(latencies) != shortN {
		t.Fatalf("isolation short sample count=%d, want=%d", len(latencies), shortN)
	}
	_, p95, p99 := capacityLatencySummary(latencies)
	for e := range shortErrors {
		if firstErr == nil {
			firstErr = e
		}
	}
	if firstErr != nil {
		t.Fatalf("isolated short HTTP failed: %v", firstErr)
	}
	for i, c := range held {
		if e := verifyCapacityHeldTunnel(c, i); e != nil {
			t.Fatalf("held CONNECT %d post-load payload failed: %v", i, e)
		}
	}
	snap, err := child.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.ActiveDownstream != heldN {
		t.Fatalf("isolated physical permits=%d want=%d", snap.ActiveDownstream, heldN)
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") == "1" {
		c := snap.ClassPools
		if c == nil || c.Long != heldN || c.Total != heldN || c.Short != 0 || c.Provisional != 0 {
			t.Fatalf("isolated class conservation: %+v", c)
		}
	} else if snap.ClassPools != nil {
		t.Fatalf("classes unexpectedly enabled: %+v", snap.ClassPools)
	}
	if peak.RSSBytes <= baseline.RSSBytes || peak.Handles < baseline.Handles {
		t.Fatalf("isolated child has no resource growth, baseline=%+v peak=%+v", baseline, peak)
	}
	if runtime.GOOS == "windows" && peak.CPUNanos <= baseline.CPUNanos {
		t.Fatalf("native child CPU instrumentation failed: baseline=%+v peak=%+v", baseline, peak)
	}
	t.Logf("ISOLATED_RESULT platform=%s seed=%d policy=%s classes=%s held_long=%d completed_short=%d short_rps=%.2f short_offered_p95=%s short_offered_p99=%s baseline_private_bytes=%d held_private_bytes=%d peak_private_bytes=%d baseline_rss_bytes=%d held_rss_bytes=%d peak_rss_bytes=%d baseline_handles=%d held_handles=%d peak_handles=%d proxy_cpu_delta_ns=%d global_hard=%d", runtime.GOOS, seed, os.Getenv("PXGO_RESEARCH_CAPACITY_POLICY"), os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES"), heldN, shortN, float64(shortN)/elapsed.Seconds(), p95, p99, baseline.PrivateBytes, heldMetrics.PrivateBytes, peak.PrivateBytes, baseline.RSSBytes, heldMetrics.RSSBytes, peak.RSSBytes, baseline.Handles, heldMetrics.Handles, peak.Handles, peak.CPUNanos-baseline.CPUNanos, snap.HardEnvelope)
	for i, c := range held {
		_ = c.Close()
		held[i] = nil
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		snap, err = child.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if snap.ActiveDownstream == 0 && (snap.ClassPools == nil || snap.ClassPools.Total == 0) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("isolated physical/class permits not fully released: %+v", snap)
}
