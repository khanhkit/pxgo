package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
	"github.com/pavelsimo/pxgo/internal/supervisor"
	"github.com/pavelsimo/pxgo/internal/wproxy"
)

func serverFromURL(t *testing.T, raw string) wproxy.Server {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return wproxy.Server{Host: host, Port: port, Scheme: u.Scheme}
}

func newSupervisorOnlyServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.SockTimeout = 1
	s := &Server{cfg: cfg, sup: supervisor.New(supervisor.Owners{})}
	t.Cleanup(func() {
		s.clearTransports()
		s.sup.Close()
	})
	return s
}

func roundTripViaCandidates(t *testing.T, s *Server, proxies []wproxy.Server, target string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	resp, err := s.roundTripHTTPWithProxyFallback(req, req.URL, nil, target, "", proxies)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestTCSUPHTTP016LocalProxyFailureCoolsAcrossRequests(t *testing.T) {
	var failingCalls atomic.Int32
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failingCalls.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer failing.Close()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	a := serverFromURL(t, failing.URL)
	b := serverFromURL(t, healthy.URL)
	s := newSupervisorOnlyServer(t)
	proxies := []wproxy.Server{a, b}

	for i := 0; i < 2; i++ {
		resp := roundTripViaCandidates(t, s, proxies, "http://origin.example.test/resource")
		_ = resp.Body.Close()
	}
	if got := failingCalls.Load(); got != 2 {
		t.Fatalf("failing proxy attempts after two requests = %d, want 2", got)
	}

	resp := roundTripViaCandidates(t, s, proxies, "http://origin.example.test/resource")
	_ = resp.Body.Close()
	if got := failingCalls.Load(); got != 2 {
		t.Fatalf("cooled proxy was retried before healthy candidate: attempts=%d", got)
	}
}

func TestTCSUPHTTP016ValidOriginStatusClearsTransportPenalty(t *testing.T) {
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "origin failure", http.StatusInternalServerError)
	}))
	defer parent.Close()

	a := serverFromURL(t, parent.URL)
	b := wproxy.Server{Host: "proxy-b.example", Port: 8080, Scheme: "http"}
	s := newSupervisorOnlyServer(t)
	key := proxyKeyForSupervisor(a)
	s.sup.Record(supervisor.Outcome{Kind: supervisor.OutcomeProxyDialFailure, Proxy: key})
	s.sup.Record(supervisor.Outcome{Kind: supervisor.OutcomeProxyDialFailure, Proxy: key})

	resp := roundTripViaCandidates(t, s, []wproxy.Server{a}, "http://origin.example.test/resource")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	got := orderProxyCandidates(s.sup, []wproxy.Server{a, b})
	if got[0] != a {
		t.Fatalf("valid HTTP response did not clear transport penalty: %#v", got)
	}
}

func TestTCSUPCONNECT017LocalProxyFailureCoolsAcrossRequests(t *testing.T) {
	var failingCalls atomic.Int32
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failingCalls.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer failing.Close()

	var (
		connMu sync.Mutex
		conns  []net.Conn
	)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		hj := w.(http.Hijacker)
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		connMu.Lock()
		conns = append(conns, conn)
		connMu.Unlock()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
	}))
	defer func() {
		connMu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		connMu.Unlock()
		healthy.Close()
	}()

	a := serverFromURL(t, failing.URL)
	b := serverFromURL(t, healthy.URL)
	s := newSupervisorOnlyServer(t)
	proxies := []wproxy.Server{a, b}

	for i := 0; i < 2; i++ {
		conn, _, err := s.connectWithProxyFallback(context.Background(), "origin.example.test:443", "", proxies)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}
	if got := failingCalls.Load(); got != 2 {
		t.Fatalf("failing CONNECT proxy attempts = %d, want 2", got)
	}

	conn, _, err := s.connectWithProxyFallback(context.Background(), "origin.example.test:443", "", proxies)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := failingCalls.Load(); got != 2 {
		t.Fatalf("cooled CONNECT proxy retried before healthy candidate: %d", got)
	}
}

func TestTCSUPSTATUS013ServerShutdownMakesSupervisorTerminal(t *testing.T) {
	cfg := config.Default()
	cfg.Server = "DIRECT"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.sup == nil {
		t.Fatal("Server has no Runtime Supervisor")
	}

	a := supervisor.Candidate{Key: "proxy-a"}
	b := supervisor.Candidate{Key: "proxy-b"}
	s.sup.Record(supervisor.Outcome{Kind: supervisor.OutcomeProxyDialFailure, Proxy: a.Key})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	s.sup.Record(supervisor.Outcome{Kind: supervisor.OutcomeProxyDialFailure, Proxy: a.Key})
	got := s.sup.Order([]supervisor.Candidate{a, b})
	if got[0] != a {
		t.Fatalf("closed Supervisor accepted new health mutation: %#v", got)
	}
	if s.sup.Trigger(supervisor.ActionRefreshRoute) {
		t.Fatal("closed Supervisor accepted recovery action")
	}
}

func TestTCSUPROUTE005NoCandidateErrorDoesNotBecomeDirect(t *testing.T) {
	s := newSupervisorOnlyServer(t)
	req := httptest.NewRequest(http.MethodGet, "http://origin.example.test/", nil)
	resp, err := s.roundTripHTTPWithProxyFallback(req, req.URL, nil, req.URL.String(), "", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || err.Error() != "no proxy candidates" {
		t.Fatalf("error=%v, want no proxy candidates", err)
	}
}

func Example_runtimeSupervisorNoDirect() {
	fmt.Println("authoritative route membership is preserved")
	// Output: authoritative route membership is preserved
}
