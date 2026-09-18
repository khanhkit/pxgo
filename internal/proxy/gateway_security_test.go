package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
		if r.URL.Path != "/PxgoQuit" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "origin PxgoQuit resource")
	}))
	defer upstream.Close()

	px := startTestProxy(t, config.Default())
	resp, err := proxyClient(t, px.Port()).Get(upstream.URL + "/PxgoQuit")
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
	req := httptest.NewRequest(http.MethodGet, "/PxgoQuit", nil)
	req.RequestURI = "/PxgoQuit"
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
	req := httptest.NewRequest(http.MethodGet, "/PxgoQuit", nil)
	req.RequestURI = "/PxgoQuit"
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
	if srv.ReadHeaderTimeout != wantSock || srv.ReadTimeout != wantSock || srv.WriteTimeout != wantSock {
		t.Fatalf("slow-client timeout policy header=%s read=%s write=%s want=%s", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, wantSock)
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
	if err == nil {
		t.Fatal("slow header connection unexpectedly produced a response byte")
	}
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

func shutdownForTest(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
}

func statusLine(resp *http.Response) string {
	if resp == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d %s", resp.StatusCode, resp.Status)
}
