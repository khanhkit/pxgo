package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestHTTPUpgrade101RelaysBidirectionally(t *testing.T) {
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
		req, err := http.ReadRequest(reader)
		if err != nil {
			originDone <- err
			return
		}
		if !intermediaryHeaderHasToken(req.Header, headerConnection, "upgrade") || !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
			originDone <- fmt.Errorf("upgrade headers missing: %#v", req.Header)
			return
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
			originDone <- err
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
		payload := make([]byte, 4)
		if _, err := io.ReadFull(reader, payload); err != nil {
			originDone <- err
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
	_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: wrong.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", target)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status=%s", resp.Status)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
	echo := make([]byte, 4)
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("upgrade response is not bidirectional: %v", err)
	}
	if string(echo) != "ping" {
		t.Fatalf("upgrade echo=%q", echo)
	}
	if err := <-originDone; err != nil {
		t.Fatalf("origin upgrade: %v", err)
	}
}

func intermediaryHeaderHasToken(h http.Header, key, want string) bool {
	for _, value := range h.Values(key) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), want) {
				return true
			}
		}
	}
	return false
}
