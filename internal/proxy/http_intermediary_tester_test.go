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
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestAuthNonePreservesParentProxyChallenge(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()

	parentCfg := config.Default()
	parentCfg.ClientAuth = "BASIC"
	parentCfg.ClientUsername = "test"
	parentCfg.ClientPassword = "12345"
	parent := startTestProxy(t, parentCfg)

	childCfg := config.Default()
	childCfg.Server = parent.ListenAddr()
	childCfg.Auth = "NONE"
	child := startTestProxy(t, childCfg)

	resp, err := proxyClient(t, child.Port()).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status=%s want 407", resp.Status)
	}
	if challenge := resp.Header.Get("Proxy-Authenticate"); challenge == "" {
		t.Fatalf("explicit Auth=NONE lost parent Proxy-Authenticate challenge: %#v", resp.Header)
	}
}

func TestHTTPUpgradeForwardsPipelinedClientBytes(t *testing.T) {
	originLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer originLn.Close()

	originDone := make(chan error, 1)
	go func() {
		conn, err := originLn.Accept()
		if err != nil {
			originDone <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			originDone <- err
			return
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
			originDone <- err
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		payload := make([]byte, 4)
		if _, err := io.ReadFull(reader, payload); err != nil {
			originDone <- err
			return
		}
		if string(payload) != "ping" {
			originDone <- fmt.Errorf("pipelined payload=%q", payload)
			return
		}
		_, err = conn.Write(payload)
		originDone <- err
	}()

	px := startTestProxy(t, config.Default())
	conn, err := net.Dial("tcp", px.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	target := "http://" + originLn.Addr().String() + "/chat"
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: ignored.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nping", target); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status=%s", resp.Status)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	echo := make([]byte, 4)
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("reading pipelined upgrade echo: %v", err)
	}
	if string(echo) != "ping" {
		t.Fatalf("echo=%q", echo)
	}
	if err := <-originDone; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownOwnsHTTPUpgradeTunnel(t *testing.T) {
	originLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer originLn.Close()
	originDone := make(chan struct{})
	go func() {
		defer close(originDone)
		conn, err := originLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, reader)
	}()

	px := startTestProxy(t, config.Default())
	conn, err := net.Dial("tcp", px.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	target := "http://" + originLn.Addr().String() + "/chat"
	_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: ignored.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", target)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status=%s", resp.Status)
	}
	waitForActiveTunnels(t, px, 1, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := px.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := px.ActiveTunnels(); got != 0 {
		t.Fatalf("active upgrade tunnels after shutdown=%d", got)
	}
	_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if _, err := reader.Peek(1); err == nil {
		t.Fatal("upgrade client remained open after shutdown")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("upgrade client remained open after shutdown: %v", err)
	}
	select {
	case <-originDone:
	case <-time.After(time.Second):
		t.Fatal("origin upgrade connection remained open after proxy shutdown")
	}
}

func FuzzStripIntermediaryHeaders(f *testing.F) {
	for _, token := range []string{"X-Hop", "x-hop", headerKeepAlive, "X_Custom", "X-Trace-123"} {
		f.Add(token)
	}
	f.Fuzz(func(t *testing.T, token string) {
		token = strings.TrimSpace(token)
		if token == "" || len(token) > 64 || strings.ContainsAny(token, " ,\t\r\n:") {
			return
		}
		for _, r := range token {
			switch {
			case r == '-', r == '_':
			case r >= '0' && r <= '9':
			case r >= 'A' && r <= 'Z':
			case r >= 'a' && r <= 'z':
			default:
				return
			}
		}
		h := http.Header{}
		h.Add(headerConnection, " keep-alive, "+token)
		h.Set(token, "secret")
		h.Set(headerKeepAlive, "timeout=5")
		h.Set("Proxy-Custom", "secret")
		h.Set("Proxy-Authorization", "secret")
		stripIntermediaryHeaders(h, true)
		for _, name := range []string{headerConnection, token, headerKeepAlive, "Proxy-Custom", "Proxy-Authorization"} {
			if got := h.Get(name); got != "" {
				t.Fatalf("header %q survived intermediary stripping: %q", name, got)
			}
		}
	})
}
