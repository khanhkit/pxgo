package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
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
	const initial int64 = 1
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

func TestTunnelLifecycleConcurrentShutdown(t *testing.T) {
	upstreamLn := startHoldingTunnelTarget(t)
	cfg := config.Default()
	cfg.Workers = 4
	cfg.Threads = 64
	cfg.Idle = 30
	px := startTestProxy(t, cfg)

	const clients = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successfulMu sync.Mutex
	var successful []net.Conn
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			conn, err := net.DialTimeout("tcp", px.ListenAddr(), time.Second)
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstreamLn.Addr().String(), upstreamLn.Addr().String()); err != nil {
				_ = conn.Close()
				return
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
			if err != nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					_ = resp.Body.Close()
				}
				_ = conn.Close()
				return
			}
			_ = conn.SetDeadline(time.Time{})
			successfulMu.Lock()
			successful = append(successful, conn)
			successfulMu.Unlock()
		}()
	}
	close(start)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && px.ActiveTunnels() == 0 {
		time.Sleep(time.Millisecond)
	}
	if px.ActiveTunnels() == 0 {
		t.Fatal("race fixture failed to establish any tunnel before shutdown")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := px.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown during tunnel creation: %v", err)
	}
	wg.Wait()
	if got := px.ActiveTunnels(); got != 0 {
		t.Fatalf("active tunnels after concurrent shutdown=%d", got)
	}

	successfulMu.Lock()
	for _, conn := range successful {
		_ = conn.Close()
	}
	successfulMu.Unlock()
}

func TestRelayOneWaySchedulerStress(t *testing.T) {
	oldProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldProcs)

	client, proxyClient := net.Pipe()
	proxyUpstream, upstream := net.Pipe()
	defer client.Close()
	defer upstream.Close()
	go relay(proxyClient, proxyUpstream, 40*time.Millisecond)

	const chunks = 80
	writeDone := make(chan error, 1)
	go func() {
		for i := 0; i < chunks; i++ {
			if _, err := upstream.Write([]byte{byte(i)}); err != nil {
				writeDone <- err
				return
			}
			time.Sleep(8 * time.Millisecond)
		}
		writeDone <- upstream.Close()
	}()

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	data := make([]byte, chunks)
	if _, err := io.ReadFull(client, data); err != nil {
		t.Fatalf("active one-way relay died under scheduler stress: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("one-way writer: %v", err)
	}
}

func TestTunnelShutdownStress(t *testing.T) {
	count := 64
	if raw := os.Getenv("PXGO_TUNNEL_STRESS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("PXGO_TUNNEL_STRESS=%q must be a positive integer", raw)
		}
		count = parsed
	}

	upstreamLn := startHoldingTunnelTarget(t)
	cfg := config.Default()
	cfg.Workers = count
	cfg.Threads = 1
	cfg.Idle = 30
	px := startTestProxy(t, cfg)
	clients := make([]net.Conn, 0, count)
	for i := 0; i < count; i++ {
		conn, _ := openRawConnectTunnel(t, px, upstreamLn.Addr().String())
		clients = append(clients, conn)
	}
	defer func() {
		for _, conn := range clients {
			_ = conn.Close()
		}
	}()
	waitForActiveTunnels(t, px, int64(count), 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	if err := px.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown %d tunnels: %v", count, err)
	}
	if got := px.ActiveTunnels(); got != 0 {
		t.Fatalf("shutdown %d tunnels left %d active", count, got)
	}
	if elapsed := time.Since(started); elapsed >= 10*time.Second {
		t.Fatalf("shutdown %d tunnels exceeded deadline: %s", count, elapsed)
	}
}

func startHoldingTunnelTarget(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	accepted := make([]net.Conn, 0)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted = append(accepted, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range accepted {
			_ = conn.Close()
		}
	})
	return ln
}
