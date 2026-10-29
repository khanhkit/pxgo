package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// runStrictSSEActualResponse checks real HTTP response-based SSE class
// admission and Accept q=0 behavior with genuine held CONNECT TCP tunnels.
func runStrictSSEActualResponse(t *testing.T, heldLong int) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	cfg := config.Default()
	cfg.MaxConnections = 16
	cfg.ProxyReload = 0
	cfg.Idle = 45
	cfg.SockTimeout = 5
	px := startTestProxy(t, cfg)

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	held := make([]net.Conn, 0, 12)
	stopSSE := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopSSE) }) }
	defer func() {
		stop()
		for _, c := range held {
			_ = c.Close()
		}
		_ = echo.Close()
		wg.Wait()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			s := px.capacity.snapshot()
			if s.ActiveDownstream == 0 && s.ClassPools != nil && s.ClassPools.Total == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if s := px.capacity.snapshot(); s.ActiveDownstream != 0 || s.ClassPools == nil || s.ClassPools.Total != 0 {
			t.Errorf("classification/physical tokens leaked before shutdown: %+v", s)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := px.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	// Fill the opt-in post-authentication long budget with true CONNECTs.
	for i := 0; i < heldLong; i++ {
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr(), echo.Addr()); err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
		if err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			_ = c.Close()
			t.Fatalf("long[%d] status=%d", i, resp.StatusCode)
		}
		_ = c.SetReadDeadline(time.Time{})
		held = append(held, c)
	}
	if pool := px.capacity.classes.snapshot(); pool.Long != heldLong || pool.LongBudget != 12 {
		t.Fatalf("required long-cap plateau missing: %+v", pool)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{\"ok\":true}")
			return
		}
		// Upstream is allowed to ignore the client's Accept preferences.
		// A real SSE body + flush proves this is not ordinary short HTTP.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: ready\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-stopSSE:
		case <-r.Context().Done():
		}
	}))
	defer func() { stop(); origin.Close() }()

	client := proxyClient(t, px.Port())
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, origin.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if heldLong == 12 {
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("actual SSE MUST be rejected once long budget full, got %s", resp.Status)
		}
		if got := px.capacity.classes.snapshot(); got.Long != 12 || got.LongDenied != 1 {
			t.Fatalf("SSE response long quota denial missing: %+v", got)
		}
	} else {
		if resp.StatusCode != http.StatusOK ||
			!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("actual SSE below long cap should stream: %s %q", resp.Status, resp.Header.Get("Content-Type"))
		}
		first, err := bufio.NewReader(resp.Body).ReadString('\n')
		if err != nil || first != "data: ready\n" {
			t.Fatalf("first real SSE event unavailable: %q err=%v", first, err)
		}
		if got := px.capacity.classes.snapshot(); got.Long != 12 || got.Short != 0 || got.LongDenied != 0 {
			t.Fatalf("actual SSE must be reclassified as long under budget: %+v", got)
		}
	}
	jsonReq, err := http.NewRequest(http.MethodGet, origin.URL+"/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	jsonReq.Header.Set("Accept", "application/json, text/event-stream;q=0")
	jsonResp, err := client.Do(jsonReq)
	if err != nil {
		t.Fatal(err)
	}
	defer jsonResp.Body.Close()
	if jsonResp.StatusCode != http.StatusOK {
		t.Fatalf("unacceptable q=0 SSE token falsely denied JSON: %s", jsonResp.Status)
	}
	data, err := io.ReadAll(jsonResp.Body)
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("JSON response corrupted: %q err=%v", data, err)
	}
	denied := uint64(0)
	if heldLong == 12 {
		denied = 1
	}
	if got := px.capacity.classes.snapshot(); got.Long != 12 || got.LongDenied != denied {
		t.Fatalf("true SSE response/quota and q=0 JSON invariants violated: %+v", got)
	}
	t.Logf("STRICT_SSE_RESPONSE_RESULT long_plateau=%d actual_sse_response_classified=true json_q_zero_allowed=true first_event_flushed=%t", heldLong, heldLong == 11)
}

func TestStrictLongIntakeUsesActualSSEResponseAndAcceptQuality(t *testing.T) {
	for _, held := range []int{11, 12} {
		t.Run(fmt.Sprintf("held_long_%d", held), func(t *testing.T) {
			runStrictSSEActualResponse(t, held)
		})
	}
}

// TestAcceptsEventStreamQualitySemantics prevents a q=0 token from
// triggering a false long-intake 503 before a genuine JSON response arrives.
func TestAcceptsEventStreamQualitySemantics(t *testing.T) {
	cases := []struct {
		accept string
		want   bool
	}{
		{"text/event-stream", true},
		{"text/event-stream;q=1", true},
		{"TEXT/EVENT-STREAM; q=0.3", true},
		{"application/json, text/event-stream;q=0.4", true},
		{"application/json, text/event-stream;q=0", false},
		{"text/event-stream;q=0.0", false},
		{"text/event-stream;Q=0", false},
		{"text/event-stream;q=-0.5", false},
		{"text/event-stream;q=1.5", false},
		{"text/event-stream;q=bogus", false},
		{"text/event-stream;q=1e0", false},
		{"text/event-stream;q=0x1p0", false},
		{"text/event-stream;q=0.0001", false},
		{"text/event-stream;q=1.001", false},
		{"text/event-stream;q=1.000", true},
		{"text/event-stream;q=0.001", true},
		{"application/json", false},
	}
	for _, tc := range cases {
		t.Run(tc.accept, func(t *testing.T) {
			header := make(http.Header)
			header.Set("Accept", tc.accept)
			if got := acceptsEventStream(header); got != tc.want {
				t.Fatalf("Accept=%q got explicit SSE=%t want=%t", tc.accept, got, tc.want)
			}
		})
	}
}
