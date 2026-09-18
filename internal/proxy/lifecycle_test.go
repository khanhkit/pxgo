package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
	"github.com/pavelsimo/pxgo/internal/wproxy"
)

func TestAPISS0001TLSHandshakeTimeoutUsesSockTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.SockTimeout = 0.05
	s := &Server{cfg: cfg}

	transport := s.newHTTPTransport(wproxy.Direct)
	want := 50 * time.Millisecond
	if transport.TLSHandshakeTimeout != want {
		t.Fatalf("TLSHandshakeTimeout=%v, want %v", transport.TLSHandshakeTimeout, want)
	}
}

func TestAPISS0001SOCKSHandshakeHonorsContextCancellation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	start := time.Now()
	conn, err := dialSOCKS5(ctx, ln.Addr().String(), "example.com:443", 200*time.Millisecond)
	if conn != nil {
		_ = conn.Close()
	}
	elapsed := time.Since(start)

	select {
	case serverConn := <-accepted:
		_ = serverConn.Close()
	default:
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dialSOCKS5 error=%v, want context.Canceled", err)
	}
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("cancellation took %v, want <150ms", elapsed)
	}
}

func TestAPISS0001ConnectAuthBodyIsSizeBounded(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		reader := bufio.NewReader(server)
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		_, _ = fmt.Fprintf(server,
			"HTTP/1.1 407 Proxy Authentication Required\r\n"+
				"Proxy-Authenticate: Basic realm=\"test\"\r\n"+
				"Content-Length: %d\r\n\r\n",
			1<<20,
		)
		_, _ = server.Write(make([]byte, 70<<10))
	}()

	cfg := config.Default()
	cfg.Auth = "BASIC"
	cfg.Username = "user"
	cfg.Password = "secret"
	_ = client.SetDeadline(time.Now().Add(300 * time.Millisecond))

	_, err := sendUpstreamConnectWithAuth(client, "example.com:443", cfg, "", "", nil)
	if err == nil {
		t.Fatal("expected oversized 407 body to fail")
	}
	if !strings.Contains(err.Error(), "407 body exceeds") {
		t.Fatalf("error=%q, want bounded-body failure", err)
	}
}
