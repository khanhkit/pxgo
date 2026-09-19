package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
)

func TestGatewayRejectsOpenUnauthenticatedDefault(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway = true
	if _, err := New(cfg); err == nil {
		t.Fatal("gateway accepted wildcard allow with client auth disabled")
	}
}

func TestGatewayAllowsExplicitRestrictiveAllowWithoutAuth(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway = true
	cfg.Allow = "10.0.0.0/8"
	if _, err := New(cfg); err != nil {
		t.Fatalf("restrictive gateway allow-list should be sufficient explicit admission policy: %v", err)
	}
}

func TestGatewayAllowsAnySafeWithCredentials(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway = true
	cfg.ClientAuth = "ANYSAFE"
	cfg.ClientUsername = "gateway-user"
	cfg.ClientPassword = "gateway-secret"
	if _, err := New(cfg); err != nil {
		t.Fatalf("authenticated gateway with no Basic challenge should be accepted: %v", err)
	}
}

func TestGatewayRejectsPlaintextBasicCapableAuth(t *testing.T) {
	for _, mode := range []string{"BASIC", "ANY", "DIGEST,BASIC"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.Gateway = true
			cfg.ClientAuth = mode
			cfg.ClientUsername = "gateway-user"
			cfg.ClientPassword = "gateway-secret"
			if _, err := New(cfg); err == nil {
				t.Fatalf("plaintext gateway accepted Basic-capable auth mode %q", mode)
			}
		})
	}
}

func TestDownstreamPasswordAuthRequiresCredentials(t *testing.T) {
	for _, mode := range []string{"BASIC", "DIGEST", "NTLM", "NEGOTIATE", "ANY", "ANYSAFE"} {
		t.Run(mode+"/missing-user", func(t *testing.T) {
			cfg := config.Default()
			cfg.ClientAuth = mode
			cfg.ClientPassword = "secret"
			if _, err := New(cfg); err == nil {
				t.Fatalf("client auth %q accepted empty username", mode)
			}
		})
		t.Run(mode+"/missing-password", func(t *testing.T) {
			cfg := config.Default()
			cfg.ClientAuth = mode
			cfg.ClientUsername = "user"
			if _, err := New(cfg); err == nil {
				t.Fatalf("client auth %q accepted empty password", mode)
			}
		})
	}
}

func TestAbsoluteProxyURLPxgoQuitIsForwardedNotControl(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != quitControlPath {
			t.Fatalf("path=%q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "origin PxgoQuit resource")
	}))
	defer upstream.Close()

	px := startTestProxy(t, config.Default())
	resp, err := proxyClient(t, px.Port()).Get(upstream.URL + quitControlPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "origin PxgoQuit resource" {
		t.Fatalf("absolute-form /PxgoQuit was consumed as local control: status=%s body=%q", resp.Status, body)
	}
	select {
	case <-px.closed:
		t.Fatal("absolute proxy URL /PxgoQuit shut down the local proxy")
	default:
	}
}

func TestRemoteOriginFormPxgoQuitCannotControlProxy(t *testing.T) {
	s, err := New(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, quitControlPath, nil)
	req.RequestURI = quitControlPath
	req.RemoteAddr = "203.0.113.10:44444"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote control request status=%d want 403", rec.Code)
	}
	timer := time.NewTimer(120 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-s.closed:
		t.Fatal("remote origin-form /PxgoQuit shut down proxy")
	case <-timer.C:
	}
}

func TestLoopbackOriginFormPxgoQuitRemainsAvailable(t *testing.T) {
	s, err := New(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, quitControlPath, nil)
	req.RequestURI = quitControlPath
	req.RemoteAddr = "127.0.0.1:44445"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback control request status=%d want 200", rec.Code)
	}
	select {
	case <-s.closed:
	case <-time.After(time.Second):
		t.Fatal("loopback origin-form /PxgoQuit did not shut down proxy")
	}
}

func TestServerTimeoutPolicyUsesExistingConfigBudgets(t *testing.T) {
	cfg := config.Default()
	cfg.SockTimeout = 0.2
	cfg.Idle = 2
	px := startTestProxy(t, cfg)

	px.stateMu.RLock()
	srv := px.srv
	px.stateMu.RUnlock()
	if srv == nil {
		t.Fatal("HTTP server not initialized")
	}
	wantSock := 200 * time.Millisecond
	if srv.ReadHeaderTimeout != wantSock || srv.ReadTimeout != wantSock {
		t.Fatalf("slow-client read policy header=%s read=%s want=%s", srv.ReadHeaderTimeout, srv.ReadTimeout, wantSock)
	}
	if srv.WriteTimeout != 2*wantSock {
		t.Fatalf("write timeout=%s want=%s", srv.WriteTimeout, 2*wantSock)
	}
	if srv.IdleTimeout != 2*time.Second {
		t.Fatalf("idle timeout=%s want=2s", srv.IdleTimeout)
	}
}

func TestSlowHeaderConnectionIsClosedBySockTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.SockTimeout = 0.1
	px := startTestProxy(t, cfg)
	conn, err := net.Dial("tcp", px.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: local\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
	one := make([]byte, 1)
	_, err = conn.Read(one)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("slow header remained open beyond configured socktimeout: %v", err)
	}
}

func TestConnectionAdmissionUsesWorkersTimesThreads(t *testing.T) {
	cfg := config.Default()
	cfg.Workers = 1
	cfg.Threads = 1
	cfg.SockTimeout = 2
	px := startTestProxy(t, cfg)

	first, err := net.Dial("tcp", px.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := io.WriteString(first, "GET / HTTP/1.1\r\nHost: hold\r\n"); err != nil {
		t.Fatal(err)
	}

	second, err := net.Dial("tcp", px.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := io.WriteString(second, "GET http://127.0.0.1:1/ HTTP/1.1\r\nHost: 127.0.0.1:1\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(second)
	_ = second.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet}); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("second connection was admitted while workers*threads budget was exhausted: %s", resp.Status)
	} else {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("expected admission wait timeout, got %v", err)
		}
	}

	_ = first.Close()
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("second connection was not admitted after capacity released: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("post-admission status=%s want 502", resp.Status)
	}
}

func TestGatewayPolicyErrorIsActionable(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway = true
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected gateway policy error")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "gateway") || (!strings.Contains(msg, "allow") && !strings.Contains(msg, "auth")) {
		t.Fatalf("gateway rejection is not actionable: %q", err)
	}
}

func TestSlowRequestBodyIsBoundedBySockTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.SockTimeout = 0.1
	px := startTestProxy(t, cfg)
	conn, err := net.Dial("tcp", px.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "POST %s/slow HTTP/1.1\r\nHost: %s\r\nContent-Length: 100\r\nConnection: close\r\n\r\nx", upstream.URL, u.Host); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
	one := make([]byte, 1)
	_, err = conn.Read(one)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("slow request body kept downstream connection open beyond read budget: %v", err)
	}
}

func TestSlowDownstreamReaderIsBoundedByWriteTimeout(t *testing.T) {
	originDone := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 32*1024)
		for i := 0; i < 2048; i++ { // 64 MiB exceeds local socket buffering by a wide margin.
			if _, err := w.Write(chunk); err != nil {
				originDone <- err
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		originDone <- nil
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.SockTimeout = 0.1 // downstream WriteTimeout is deliberately 2x this value.
	px := startTestProxy(t, cfg)
	conn, err := net.Dial("tcp", px.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(1024)
	}
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "GET %s/large HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", upstream.URL, u.Host); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-originDone:
		if err == nil {
			t.Fatal("origin wrote entire 64 MiB despite downstream client not reading")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow downstream reader kept origin producer blocked beyond bounded write window")
	}
}

func TestServerResourceBudgetsRejectDisabledOrOverflowingValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "zero workers", mutate: func(c *config.Config) { c.Workers = 0 }},
		{name: "zero threads", mutate: func(c *config.Config) { c.Threads = 0 }},
		{name: "zero idle", mutate: func(c *config.Config) { c.Idle = 0 }},
		{name: "zero socktimeout", mutate: func(c *config.Config) { c.SockTimeout = 0 }},
		{name: "nan socktimeout", mutate: func(c *config.Config) { c.SockTimeout = math.NaN() }},
		{name: "infinite socktimeout", mutate: func(c *config.Config) { c.SockTimeout = math.Inf(1) }},
		{name: "connection budget overflow", mutate: func(c *config.Config) {
			c.Workers = int(^uint(0) >> 1)
			c.Threads = 2
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatalf("unsafe resource budget %q was accepted", tc.name)
			}
		})
	}
}

func TestBasicClientAuthAllowedOnlyOnLoopbackPlaintextListener(t *testing.T) {
	loopback := config.Default()
	loopback.ClientAuth = "BASIC"
	loopback.ClientUsername = "user"
	loopback.ClientPassword = "secret"
	if _, err := New(loopback); err != nil {
		t.Fatalf("loopback BASIC compatibility should remain available: %v", err)
	}

	remote := loopback
	remote.Listen = "0.0.0.0"
	if _, err := New(remote); err == nil {
		t.Fatal("explicit non-loopback plaintext listener accepted BASIC client auth")
	}
}
