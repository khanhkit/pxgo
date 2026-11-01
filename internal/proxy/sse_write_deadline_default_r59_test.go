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

// TestR59DefaultSSESurvives40Seconds is a staging-only negative
// regression: origin sends SSE frame 2 beyond 2*SockTimeout, then closes.
// The streaming HTTP proxy must deliver both frames over real TCP.
func TestR59DefaultSSESurvives40Seconds(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
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
	cfg.SockTimeout = 20
	cfg.ProxyReload = 0
	cfg.Idle = 55
	cfg.MaxConnections = 16
	px := startTestProxy(t, cfg)
	client := proxyClient(t, px.Port())
	client.Timeout = 65 * time.Second
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status=%s", resp.Status)
	}
	br := bufio.NewReader(resp.Body)
	for _, want := range []string{"data: first\n", "\n", "data: second\n", "\n"} {
		got, readErr := br.ReadString('\n')
		if readErr != nil || got != want {
			t.Fatalf("SSE_FRAME_DELIVERY_FAILURE want=%q got=%q err=%v", want, got, readErr)
		}
	}
	t.Log("R59 SSE first and late frame delivered through AUTO/strict-class proxy")
}
