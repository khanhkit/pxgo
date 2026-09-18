package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
)

func TestShutdownOwnsAndClosesHijackedConnectTunnel(t *testing.T) {
	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstreamLn.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := upstreamLn.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	cfg := config.Default()
	cfg.Idle = 30
	px := startTestProxy(t, cfg)
	client, reader := openRawConnectTunnel(t, px, upstreamLn.Addr().String())
	defer client.Close()
	var upstream net.Conn
	select {
	case upstream = <-accepted:
		defer upstream.Close()
	case <-time.After(time.Second):
		t.Fatal("upstream did not accept CONNECT tunnel")
	}
	waitForActiveTunnels(t, px, 1, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := px.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := px.ActiveTunnels(); got != 0 {
		t.Fatalf("shutdown returned with %d hijacked tunnel(s) still active", got)
	}

	_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, err = reader.Peek(1)
	if err == nil {
		t.Fatal("client tunnel remained readable after server shutdown")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("client tunnel remained open after server shutdown: %v", err)
	}

	_ = upstream.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	one := make([]byte, 1)
	_, err = upstream.Read(one)
	if err == nil {
		t.Fatal("upstream tunnel remained readable after server shutdown")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("upstream tunnel remained open after server shutdown: %v", err)
	}
}

func TestCopyDirectionRecordsActivityBeforeCopyReturns(t *testing.T) {
	src, writer := net.Pipe()
	drain, dst := net.Pipe()
	defer src.Close()
	defer writer.Close()
	defer drain.Close()
	defer dst.Close()

	var lastActivity atomic.Int64
	initial := time.Now().UnixNano()
	lastActivity.Store(initial)
	done := make(chan bool, 1)
	go func() { done <- copyDirection(dst, src, time.Second, &lastActivity) }()
	go func() { _, _ = io.Copy(io.Discard, drain) }()

	if _, err := writer.Write([]byte("activity")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) && lastActivity.Load() == initial {
		time.Sleep(5 * time.Millisecond)
	}
	if got := lastActivity.Load(); got == initial {
		t.Fatal("lastActivity was not updated while the copy was actively transferring bytes")
	}
	select {
	case <-done:
		t.Fatal("copyDirection returned before the activity observation; test no longer exercises in-flight accounting")
	default:
	}
}

func openRawConnectTunnel(t *testing.T, px *Server, target string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", px.ListenAddr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		_ = conn.Close()
		t.Fatalf("CONNECT status=%s", resp.Status)
	}
	return conn, reader
}

func waitForActiveTunnels(t *testing.T, px *Server, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if px.ActiveTunnels() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("active tunnels=%d want=%d", px.ActiveTunnels(), want)
}
