package proxy

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"testing"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
)

func TestResponseTrailersAreForwarded(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Checksum")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "trailer-body")
		w.Header().Set("X-Checksum", "abc123")
	}))
	defer origin.Close()
	px := startTestProxy(t, config.Default())
	resp, err := proxyClient(t, px.Port()).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Trailer.Get("X-Checksum"); got != "abc123" {
		t.Fatalf("response trailer=%q want abc123; headers=%#v trailers=%#v", got, resp.Header, resp.Trailer)
	}
}

func TestSSEFirstEventIsFlushedBeforeNextEvent(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(w, "data: second\n\n")
		flusher.Flush()
	}))
	defer origin.Close()
	px := startTestProxy(t, config.Default())
	conn, reader := rawIntermediaryProxyRequest(t, px, "GET "+origin.URL+" HTTP/1.1\r\nHost: ignored.example\r\n\r\n")
	defer conn.Close()
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("first SSE event was not flushed promptly: %v", err)
	}
	if line != "data: first\n" {
		t.Fatalf("first SSE line=%q", line)
	}
}

func TestEarlyHints103AreForwarded(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</style.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Link")
		w.Header().Set("X-Final", "yes")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	px := startTestProxy(t, config.Default())
	client := proxyClient(t, px.Port())
	var got103 textproto.MIMEHeader
	trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
		if code == http.StatusEarlyHints {
			got103 = h
		}
		return nil
	}}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, origin.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got103 == nil || got103.Get("Link") == "" {
		t.Fatalf("103 Early Hints not forwarded: %#v", got103)
	}
	if resp.Header.Get("X-Final") != "yes" {
		t.Fatalf("final response headers lost: %#v", resp.Header)
	}
}
