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
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Exercises real CONNECT negotiation and long-lived relay accounting, not just
// TCP handshakes. A large ramp is opt-in: CI still runs the portable smoke.
func TestCapacityLongLivedCONNECTRamp(t *testing.T) {
	if runtime.GOOS == "linux" && !readCapacitySample().available {
		t.Skip("Linux enforceable FD sensor unavailable")
	}
	total := 20
	if runtime.GOOS == "linux" {
		total = 50
	}
	if os.Getenv("PXGO_CAPACITY_SOAK") == "1" {
		if runtime.GOOS != "linux" {
			t.Skip("200 CONNECT AUTO ramp needs calibrated native headroom")
		}
		total = 200
	}
	mode := os.Getenv("PXGO_CAPACITY_BENCH_MODE")
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != string(modeLegacy) {
		t.Fatalf("unexpected benchmark mode %q", mode)
	}
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", mode)
	// Complete upstream EOF after client half-close; a holding origin would
	// deliberately keep the reverse relay alive until the idle deadline.
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		for {
			c, e := upstream.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(io.Discard, c) }()
		}
	}()
	cfg := config.Default()
	cfg.SockTimeout = 75 // enough for the 200-connection additive growth plateau
	cfg.Idle = 180
	cfg.ProxyReload = 0
	if mode == string(modeLegacy) {
		cfg.Workers = 1
		cfg.Threads = 256
	} // explicitly tuned control for 200 held tunnels
	px := startTestProxy(t, cfg)
	clients := make([]net.Conn, 0, total)
	latencies := make([]time.Duration, 0, total)
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	for i := 0; i < total; i++ {
		started := time.Now()
		c, err := net.DialTimeout("tcp", px.ListenAddr(), time.Second)
		if err != nil {
			t.Fatalf("CONNECT %d dial: %v", i, err)
		}
		_ = c.SetReadDeadline(time.Now().Add(65 * time.Second))
		_, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.Addr(), upstream.Addr())
		if err == nil {
			reader := bufio.NewReader(c)
			var resp *http.Response
			resp, err = http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
			if err == nil {
				if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("CONNECT status %d", resp.StatusCode)
				}
				_ = resp.Body.Close()
			}
		}
		if err != nil {
			_ = c.Close()
			t.Fatalf("CONNECT %d handshake: %v, governor=%+v", i, err, px.capacityDiagnosticSnapshot())
		}
		_ = c.SetReadDeadline(time.Time{})
		clients = append(clients, c)
		latencies = append(latencies, time.Since(started))
	}
	if got := px.ActiveTunnels(); got != int64(total) {
		t.Fatalf("tunnels=%d want=%d", got, total)
	}
	snap := px.capacityDiagnosticSnapshot()
	if snap.ActiveDownstream < total {
		t.Fatalf("downstream permits undercounted %+v", snap)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("platform=%s mode=%s CONNECT=%d p50=%s p95=%s p99=%s peakActive=%d window=%d sensor=%s", runtime.GOOS, mode, total, latencies[total/2], latencies[(95*total+99)/100-1], latencies[(99*total+99)/100-1], snap.ActiveDownstream, snap.SoftWindow, snap.SensorAvailability)
	for _, c := range clients {
		_ = c.Close()
	}
	clients = nil
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		if px.ActiveTunnels() == 0 && px.capacityDiagnosticSnapshot().ActiveDownstream == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("relay/tunnel permit leak after disconnect: tunnels=%d capacity=%+v", px.ActiveTunnels(), px.capacityDiagnosticSnapshot())
}

func TestCapacityHTTPKeepAliveLeaseOwnership(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	cfg := config.Default()
	cfg.MaxConnections = 1
	cfg.SockTimeout = 4
	cfg.ProxyReload = 0
	px := startTestProxy(t, cfg)
	// The first socket retains its sole permit during HTTP keep-alive idle time.
	c, err := net.DialTimeout("tcp", px.ListenAddr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = fmt.Fprintf(c, "GET http://127.0.0.1:1/test HTTP/1.1\r\nHost: 127.0.0.1:1\r\nConnection: keep-alive\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(c)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if snap := px.capacityDiagnosticSnapshot(); snap.ActiveDownstream != 1 {
		t.Fatalf("keep-alive lost its permit: %+v", snap)
	}
	_ = c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for px.capacityDiagnosticSnapshot().ActiveDownstream != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("keepalive leaked permit: %+v", px.capacityDiagnosticSnapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
