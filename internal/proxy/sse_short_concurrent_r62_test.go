package proxy

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestR62SSERemainsLiveWhileShortHTTPProceedsPastWriteTimeout(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{\"ok\":true}")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-time.After(42 * time.Second):
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: second\n\n")
		w.(http.Flusher).Flush()
	}))
	defer origin.Close()
	cfg := config.Default()
	cfg.MaxConnections = 16
	cfg.ProxyReload = 0
	cfg.SockTimeout = 20
	cfg.Idle = 60
	px := startTestProxy(t, cfg)
	streamClient := proxyClient(t, px.Port())
	streamClient.Timeout = 70 * time.Second
	defer streamClient.CloseIdleConnections()
	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/events", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := streamClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status=%s", resp.Status)
	}
	sse := bufio.NewReader(resp.Body)
	for _, want := range []string{"data: first\n", "\n"} {
		got, e := sse.ReadString('\n')
		if e != nil || got != want {
			t.Fatalf("first SSE=%q err=%v", got, e)
		}
	}
	shortClient := proxyClient(t, px.Port())
	shortClient.Timeout = 8 * time.Second
	defer shortClient.CloseIdleConnections()
	for i := 0; i < 12; i++ {
		time.Sleep(3 * time.Second)
		r, e := shortClient.Get(origin.URL + "/json")
		if e != nil {
			t.Fatalf("short[%d] err=%v", i, e)
		}
		b, readErr := io.ReadAll(r.Body)
		closeErr := r.Body.Close()
		if r.StatusCode != 200 || readErr != nil || closeErr != nil || string(b) != "{\"ok\":true}" {
			t.Fatalf("short[%d] status=%s body=%q read=%v close=%v", i, r.Status, b, readErr, closeErr)
		}
	}
	for _, want := range []string{"data: second\n", "\n"} {
		got, e := sse.ReadString('\n')
		if e != nil || got != want {
			t.Fatalf("late SSE=%q err=%v", got, e)
		}
	}
	if stats := px.capacity.classes.snapshot(); stats.Long < 1 || stats.LongDenied != 0 {
		t.Fatalf("SSE long or short budget corrupted: %+v", stats)
	}
	t.Log("R62_SSE_SHORT_RESULT late_frame_after_42s=true successful_short=12 real_sse_long=true")
}
