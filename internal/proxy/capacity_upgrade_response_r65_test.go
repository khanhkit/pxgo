package proxy

import (
	"bufio"
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

func TestR65StrictWebSocketUsesUpstream101NotRequestHint(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/switch" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{\"ok\":true}")
			return
		}
		c, bw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.WriteString(bw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = bw.Flush()
		_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
		_, _ = bufio.NewReader(c).ReadByte()
	}))
	defer origin.Close()
	cfg := config.Default()
	cfg.MaxConnections = 16
	cfg.ProxyReload = 0
	cfg.Idle = 40
	cfg.SockTimeout = 8
	px := startTestProxy(t, cfg)
	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < 12; i++ {
		c, e := net.DialTimeout("tcp", px.ListenAddr(), 3*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		_, e = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.Listener.Addr().String(), origin.Listener.Addr().String())
		if e != nil {
			_ = c.Close()
			t.Fatal(e)
		}
		_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
		response, e := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
		if e != nil {
			_ = c.Close()
			t.Fatal(e)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			_ = c.Close()
			t.Fatalf("CONNECT[%d] %s", i, response.Status)
		}
		_ = c.SetReadDeadline(time.Time{})
		held = append(held, c)
	}
	if s := px.capacity.classes.snapshot(); s.Long != 12 || s.LongBudget != 12 {
		t.Fatalf("plateau %+v", s)
	}
	client := proxyClient(t, px.Port())
	defer client.CloseIdleConnections()
	reqFor := func(path string) *http.Request {
		req, e := http.NewRequest(http.MethodGet, origin.URL+path, nil)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		return req
	}
	jsonResp, e := client.Do(reqFor("/json"))
	if e != nil {
		t.Fatal(e)
	}
	jsonData, readErr := io.ReadAll(jsonResp.Body)
	_ = jsonResp.Body.Close()
	if readErr != nil || jsonResp.StatusCode != http.StatusOK || string(jsonData) != "{\"ok\":true}" {
		t.Fatalf("real JSON 200 must pass: status=%s body=%q read=%v", jsonResp.Status, jsonData, readErr)
	}
	switchResp, e := client.Do(reqFor("/switch"))
	if e != nil {
		t.Fatal(e)
	}
	switchData, readErr := io.ReadAll(switchResp.Body)
	_ = switchResp.Body.Close()
	if readErr != nil || switchResp.StatusCode != http.StatusServiceUnavailable || switchResp.Header.Get("Retry-After") != "1" || !strings.Contains(string(switchData), "upgrade long response admission limit") {
		t.Fatalf("real upstream 101 must deny: status=%s retry=%q body=%q read=%v", switchResp.Status, switchResp.Header.Get("Retry-After"), switchData, readErr)
	}
	if s := px.capacity.classes.snapshot(); s.Long != 12 || s.LongDenied != 1 {
		t.Fatalf("leak or denial %+v", s)
	}
	t.Log("R65_UPGRADE_RESULT held_long=12 actual_json200=allowed actual_101=503 retry_after=1")
}
