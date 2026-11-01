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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestR67Strict192LongShortLinuxSoak(t *testing.T) {
	raw := os.Getenv("PXGO_R67_SOAK_SECONDS")
	if raw == "" {
		t.Skip("opt-in bounded Linux real-socket soak")
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 10 || seconds > 120 || seconds%5 != 0 {
		t.Fatal("PXGO_R67_SOAK_SECONDS must be a multiple of 5, within [10,120]")
	}
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"ok\":true}")
	}))
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

	cfg := config.Default()
	cfg.MaxConnections = 256
	cfg.ProxyReload = 0
	cfg.SockTimeout = 180
	cfg.Idle = 180
	px := startTestProxy(t, cfg)
	held := make([]net.Conn, 0, 192)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
		_ = echo.Close()
		wg.Wait()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if s := px.capacity.snapshot(); s.ActiveDownstream == 0 {
				if cs := px.capacity.classes.snapshot(); cs.Total == 0 {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("R67_FINISH_LEAK physical=%+v classes=%+v", px.capacity.snapshot(), px.capacity.classes.snapshot())
	}()
	for i := 0; i < 192; i++ {
		c, e := capacityMixedConnect(px.ListenAddr(), echo.Addr().String(), i)
		if e != nil {
			t.Fatalf("long CONNECT[%d] err=%v", i, e)
		}
		held = append(held, c)
	}
	if c := px.capacity.classes.snapshot(); c.Long != 192 || c.LongBudget != 192 || c.Total != 192 {
		t.Fatalf("192/192 strict long admission missing: %+v", c)
	}
	// Real denied 193rd CONNECT tests the class cap while the shared physical
	// limit has 64 free slots; origin has not lost an existing authenticated tunnel.
	probe, e := net.DialTimeout("tcp", px.ListenAddr(), 3*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	_, e = fmt.Fprintf(probe, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr(), echo.Addr())
	if e != nil {
		_ = probe.Close()
		t.Fatal(e)
	}
	_ = probe.SetReadDeadline(time.Now().Add(4 * time.Second))
	denied, e := http.ReadResponse(bufio.NewReader(probe), &http.Request{Method: http.MethodConnect})
	if e != nil {
		_ = probe.Close()
		t.Fatal(e)
	}
	_ = denied.Body.Close()
	_ = probe.Close()
	if denied.StatusCode != http.StatusServiceUnavailable || denied.Header.Get("Retry-After") != "1" {
		t.Fatalf("193rd CONNECT should deny 503 retry1, got %s retry=%q", denied.Status, denied.Header.Get("Retry-After"))
	}
	client := proxyClient(t, px.Port())
	client.Timeout = 5 * time.Second
	client.Transport.(*http.Transport).DisableKeepAlives = true
	defer client.CloseIdleConnections()

	var shorts int
	for cycle := 0; cycle < seconds/5; cycle++ {
		for i, c := range held {
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			if _, e := c.Write([]byte{byte(i)}); e != nil {
				t.Fatalf("heartbeat write cycle=%d long=%d err=%v", cycle, i, e)
			}
			var b [1]byte
			if _, e := io.ReadFull(c, b[:]); e != nil || b[0] != byte(i) {
				t.Fatalf("heartbeat cycle=%d long=%d value=%d err=%v", cycle, i, b[0], e)
			}
			_ = c.SetDeadline(time.Time{})
		}
		for j := 0; j < 12; j++ {
			resp, e := client.Get(origin.URL + "/json")
			if e != nil {
				t.Fatalf("short cycle=%d index=%d err=%v", cycle, j, e)
			}
			body, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil || resp.StatusCode != 200 || string(body) != "{\"ok\":true}" {
				t.Fatalf("short cycle=%d index=%d status=%s body=%q read=%v close=%v", cycle, j, resp.Status, body, readErr, closeErr)
			}
			shorts++
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		s := px.capacity.snapshot()
		c := px.capacity.classes.snapshot()
		if s.ActiveDownstream > 256 || s.ActiveDownstream < 192 || c.Long != 192 || c.LongDenied < 1 {
			t.Fatalf("cycle=%d leak/quota violation global=%+v class=%+v", cycle, s, c)
		}
		t.Logf("R67_192_SOAK_SAMPLE cycle=%d shorts=%d physical=%d effective=%d long=%d longDenied=%d heapAlloc=%d", cycle, shorts, s.ActiveDownstream, s.EffectiveLimit, c.Long, c.LongDenied, mem.HeapAlloc)
		time.Sleep(5 * time.Second)
	}
	t.Logf("R67_192_SOAK_RESULT held_long=192 long_budget=192 denied_extra_long=true successful_short=%d elapsed_target=%ds capped_physical=256", shorts, seconds)
}
