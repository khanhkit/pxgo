package proxy

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/wproxy"
	"golang.org/x/net/dns/dnsmessage"
)

const systemRuleResolver = "system"

// A parent proxy rejecting CONNECT is enough to prove that DoH no longer
// attempts direct 1.1.1.1:443. No network access or TLS bypass is needed.
func TestDoHBootstrapUsesUpstreamCONNECT(t *testing.T) {
	var connects atomic.Int32
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "203.0.113.77:443" {
			t.Errorf("upstream method=%q host=%q", r.Method, r.Host)
		}
		connects.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer parent.Close()
	parentAddr := strings.TrimPrefix(parent.URL, "http://")
	_, parentPort, err := net.SplitHostPort(parentAddr)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"manual", "PAC", "rules", "mixed-rules"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.DNS = "https://203.0.113.77/dns-query"
			cfg.SockTimeout = 0.2
			if mode == "manual" || mode == "rules" || mode == "mixed-rules" {
				cfg.Server = "localhost:" + parentPort // system DNS resolves proxy hostname
				if mode == "rules" {
					cfg.DNSRules = []config.DNSRule{{Resolver: cfg.DNS, Only: []string{"*.test"}}}
				}
				if mode == "mixed-rules" {
					cfg.DNSRules = []config.DNSRule{{Resolver: systemRuleResolver, Only: []string{"*.ordinary.example"}}, {Resolver: cfg.DNS, Only: []string{"*.test"}}}
				}
			} else {
				pacPath := filepath.Join(t.TempDir(), "proxy.pac")
				pacContent := fmt.Sprintf(`function FindProxyForURL(url, host) { return "PROXY localhost:%s"; }`, parentPort)
				if err := os.WriteFile(pacPath, []byte(pacContent), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg.PAC = pacPath
			}
			px, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer px.Shutdown(context.Background())
			before := connects.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := px.dnsResolver.LookupIP(ctx, "bootstrap-fixture.test"); err == nil {
				t.Fatal("expected parent proxy CONNECT rejection")
			}
			if connects.Load() == before {
				t.Fatalf("DoH did not use parent CONNECT via %s routing", mode)
			}
		})
	}
}

func TestDoHBootstrapRejectsLocalPxGoProxyLoop(t *testing.T) {
	cfg := config.Default()
	cfg.DNS = "https://203.0.113.77/dns-query"
	cfg.Server = fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	cfg.SockTimeout = 0.2
	px, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer px.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := px.dnsResolver.LookupIP(ctx, "self-loop.test"); err == nil {
		t.Fatal("expected fail-closed bootstrap loop rejection")
	}
}

func TestDoHBootstrapSafeDialRejectsLocalInterfaceAndAliases(t *testing.T) {
	const listenPort = 3128
	var dialed atomic.Int32
	neverDial := func(context.Context, string, string) (net.Conn, error) {
		if dialed.Add(1)%2 == 0 {
			return nil, fmt.Errorf("unexpected outbound dial")
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	names := []string{"127.0.0.1", "localhost", "localhost.", "::1", "::ffff:127.0.0.1"}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range addrs {
		if n, ok := addr.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsUnspecified() {
			names = append(names, n.IP.String())
			break
		}
	}
	for _, host := range names {
		t.Run(host, func(t *testing.T) {
			p := wproxy.Server{Host: host, Port: listenPort, Scheme: "http"}
			dial := newDoHBootstrapSafeDialer([]wproxy.Server{p}, listenPort, neverDial)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := dial(ctx, "tcp", net.JoinHostPort(host, "3128"))
			if err == nil || !strings.Contains(err.Error(), "PxGo listener") {
				t.Fatalf("host=%s, got error=%v, expected loop rejection", host, err)
			}
		})
	}
	if got := dialed.Load(); got != 0 {
		t.Fatalf("self-proxy dial attempts=%d want zero", got)
	}
}

func TestDoHBootstrapSafeDialPreservesOtherPortAndPinnedIP(t *testing.T) {
	proxy := wproxy.Server{Host: "localhost", Port: 8132, Scheme: "http"}
	called := false
	dial := newDoHBootstrapSafeDialer([]wproxy.Server{proxy}, 3128, func(_ context.Context, _, address string) (net.Conn, error) {
		called = true
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			t.Fatal(err)
		}
		if net.ParseIP(host) == nil || port != "8132" {
			t.Fatalf("not pinned to resolved IP: %q", address)
		}
		return nil, errors.New("synthetic dial failure")
	})
	_, err := dial(context.Background(), "tcp", "localhost:8132")
	if err == nil || !called {
		t.Fatalf("should attempt pinned IP, called=%v err=%v", called, err)
	}
}

func TestDoHBufferedConnPreservesCONNECTLeftover(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	conn := &dohBufferedConn{Conn: a, pending: bytes.NewReader([]byte("hello "))}
	go func() { _, _ = b.Write([]byte("world")) }()
	buf := make([]byte, 11)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello world" {
		t.Fatalf("recovered TLS prebuffer=%q", buf)
	}
}

// E2E: use PxGo's actual upstream Basic CONNECT auth to transport a verified
// TLS DoH POST and parse the DNS answer. This is fully local and deterministic.
func TestDoHBootstrapAuthenticatedCONNECTTLSRoundTrip(t *testing.T) {
	var connectHits atomic.Int32
	var dohHits atomic.Int32
	dohServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dohHits.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/dns-query" || r.Header.Get("Content-Type") != "application/dns-message" {
			t.Errorf("unexpected DoH request: %s %s content-type=%s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		wire, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		msg := dnsmessage.Message{}
		if err := msg.Unpack(wire); err != nil {
			t.Error(err)
			return
		}
		if len(msg.Questions) != 1 {
			t.Errorf("questions=%v", msg.Questions)
			return
		}
		msg.Response = true
		msg.RecursionAvailable = true
		if msg.Questions[0].Type == dnsmessage.TypeA {
			msg.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: msg.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 15}, Body: &dnsmessage.AResource{A: [4]byte{198, 51, 100, 42}}}}
		}
		wire, err = msg.Pack()
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	}))
	defer dohServer.Close()
	parent := newPersistentConnectTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectHits.Add(1)
		if r.Method != http.MethodConnect || r.Host != strings.TrimPrefix(dohServer.URL, "https://") {
			t.Errorf("CONNECT target=%s method=%s", r.Host, r.Method)
		}
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("test:12345")) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="PxClient"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer parent.Close()
	cfg := config.Default()
	cfg.DNS = dohServer.URL + "/dns-query"
	cfg.Server = strings.TrimPrefix(parent.URL, "http://")
	cfg.Auth = "BASIC"
	cfg.Username = "test"
	cfg.Password = "12345"
	cfg.SockTimeout = 2

	for _, mode := range []string{"trusted-cert-success", "trusted-rules-success"} {
		t.Run(mode, func(t *testing.T) {
			cfg := cfg
			if mode == "trusted-rules-success" {
				cfg.DNSRules = []config.DNSRule{{Resolver: systemRuleResolver, Only: []string{"*.ordinary.example"}}, {Resolver: cfg.DNS, Only: []string{"*.test"}}}
			}
			px, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer px.Shutdown(context.Background())
			roots := x509.NewCertPool()
			roots.AddCert(dohServer.Certificate())
			px.dnsResolver.SetDoHRootCAs(roots)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			got, err := px.dnsResolver.LookupIP(ctx, "bootstrap-integration.test")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 || !got[0].Equal(net.IPv4(198, 51, 100, 42)) {
				t.Fatalf("DoH response addresses=%v", got)
			}
			if dohHits.Load() == 0 || connectHits.Load() == 0 {
				t.Fatalf("DoH hits=%d CONNECT hits=%d", dohHits.Load(), connectHits.Load())
			}
		})
	}
	t.Run("untrusted-cert-rejected", func(t *testing.T) {
		px, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer px.Shutdown(context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := px.dnsResolver.LookupIP(ctx, "untrusted-cert.test"); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("expected TLS certificate rejection, err=%v", err)
		}
	})
}

func TestDoHBootstrapPACReloadKeepsRoutingConsistent(t *testing.T) {
	var firstHits, secondHits atomic.Int32
	firstEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	}()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		select {
		case firstEntered <- struct{}{}:
		default:
		}
		select {
		case <-releaseFirst:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer second.Close()
	var script atomic.Value
	setScript := func(proxyURL string) {
		script.Store(fmt.Sprintf(`function FindProxyForURL(url, host) { return "PROXY %s"; }`, strings.TrimPrefix(proxyURL, "http://")))
	}
	setScript(first.URL)
	var pacUnavailable atomic.Bool
	pacServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if pacUnavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, script.Load().(string))
	}))
	defer pacServer.Close()
	cfg := config.Default()
	cfg.DNS = "https://203.0.113.77/dns-query"
	cfg.PAC = pacServer.URL
	cfg.ProxyReload = 1
	cfg.SockTimeout = 2
	px, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer px.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { _, err := px.dnsResolver.LookupIP(ctx, "pac-before-reload.test"); firstDone <- err }()
	select {
	case <-firstEntered:
	case <-ctx.Done():
		t.Fatal("first proxy not reached")
	}
	setScript(second.URL)
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- px.reloadProxy(ctx, true) }()
	select {
	case err := <-reloadDone:
		t.Fatalf("reload retired active bootstrap early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err == nil {
		t.Fatal("expected first proxy to reject CONNECT")
	}
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}
	before := firstHits.Load()
	if _, err := px.dnsResolver.LookupIP(ctx, "pac-after-reload.test"); err == nil {
		t.Fatal("expected second proxy to reject CONNECT")
	}
	if secondHits.Load() == 0 || firstHits.Load() != before {
		t.Fatalf("stale DoH routing after reload: first=%d (was %d), second=%d", firstHits.Load(), before, secondHits.Load())
	}
	// A failed PAC reload must leave both ordinary and bootstrap routes intact.
	pacUnavailable.Store(true)
	if err := px.reloadProxy(ctx, true); err == nil {
		t.Fatal("invalid PAC should abort both route updates")
	}
	if _, err := px.dnsResolver.LookupIP(ctx, "pac-after-reload-failed.test"); err == nil {
		t.Fatal("expected retained proxy to reject CONNECT")
	}
	if secondHits.Load() < 2 {
		t.Fatalf("valid bootstrap route was discarded: second hits=%d", secondHits.Load())
	}
}

// A slow PAC fetch begun before Shutdown must not resurrect either routing
// snapshot after the server has entered its terminal state.
func TestDoHBootstrapPACReloadCannotResurrectAfterShutdown(t *testing.T) {
	var fetches atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	pacServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fetches.Add(1) == 3 {
			close(entered) // first two requests built the initial ordinary+bootstrap proxies
			<-release
		}
		_, _ = io.WriteString(w, `function FindProxyForURL(url, host) { return "DIRECT"; }`)
	}))
	defer pacServer.Close()
	cfg := config.Default()
	cfg.DNS = "https://203.0.113.77/dns-query"
	cfg.PAC = pacServer.URL
	cfg.ProxyReload = 1
	cfg.SockTimeout = 2
	px, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- px.reloadProxy(ctx, true) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("PAC reload did not start")
	}
	if err := px.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-reloadDone:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("reload error=%v, want terminal fence", err)
		}
	case <-ctx.Done():
		t.Fatal("reload did not observe shutdown fence")
	}
	px.wmu.RLock()
	defer px.wmu.RUnlock()
	if !px.routingClosed || px.w != nil {
		t.Fatalf("ordinary route resurrected after shutdown: closed=%v, route=%p", px.routingClosed, px.w)
	}
	if px.dohBootstrap == nil {
		t.Fatal("expected a DoH bootstrap route holder")
	}
	px.dohBootstrap.mu.RLock()
	defer px.dohBootstrap.mu.RUnlock()
	if px.dohBootstrap.current != nil {
		t.Fatal("DoH bootstrap route resurrected after shutdown")
	}
}
