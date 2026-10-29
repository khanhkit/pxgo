package proxy

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// TestSSEFirstEventMustArriveBeforeUpstreamUnblocks is a strict streaming
// integrity test: the origin must NOT send its second SSE frame until the
// client has received the first frame. This cannot pass only because the
// whole upstream body eventually completes and the proxy buffers both frames.
func TestSSEFirstEventMustArriveBeforeUpstreamUnblocks(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: ready\n\n")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		flusher.Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, "data: done\n\n")
			flusher.Flush()
		case <-r.Context().Done():
		}
	}))
	defer func() { unblock(); origin.Close() }()

	cfg := config.Default()
	cfg.ProxyReload = 0
	px := startTestProxy(t, cfg)
	conn, br := rawIntermediaryProxyRequest(t, px,
		"GET "+origin.URL+" HTTP/1.1\r\nHost: ignored.example\r\nAccept: application/json\r\nConnection: close\r\n\r\n")
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(1300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("SSE headers were not flushed before upstream second event: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE status=%s content-type=%q", resp.Status, resp.Header.Get("Content-Type"))
	}
	sse := bufio.NewReader(resp.Body)
	first, err := sse.ReadString('\n')
	if err != nil || first != "data: ready\n" {
		t.Fatalf("SSE first payload not delivered while origin blocked: %q err=%v", first, err)
	}
	separator, err := sse.ReadString('\n')
	if err != nil || separator != "\n" {
		t.Fatalf("SSE first frame separator not delivered: %q err=%v", separator, err)
	}
	// Only now does the upstream emit frame #2.
	unblock()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	last, err := io.ReadAll(sse)
	if err != nil || string(last) != "data: done\n\n" {
		t.Fatalf("SSE trailing frame lost/corrupted: %q err=%v", last, err)
	}
	t.Logf("SSE_EARLY_FLUSH_RESULT first_frame_before_upstream_release=true second_frame_after_release=true content_type_params=true real_tcp=true")
}
