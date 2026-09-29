package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"golang.org/x/net/dns/dnsmessage"
)

func TestAPISS0031CustomDNSCoversDirectHTTPAndCONNECT(t *testing.T) {
	dnsAddr, queries, stopDNS := startProxyDNSFixture(t, false)
	defer stopDNS()
	cfg := config.Default()
	cfg.Server = "DIRECT"
	cfg.DNS = "udp://" + dnsAddr
	px := startTestProxy(t, cfg)
	client := proxyClient(t, px.Port())

	httpOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "custom-dns-http")
	}))
	defer httpOrigin.Close()
	httpURL := fixtureHostURL(t, httpOrigin.URL, "origin-http.fixture.test")
	resp, err := client.Get(httpURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "custom-dns-http" {
		t.Fatalf("HTTP status=%s body=%q", resp.Status, body)
	}

	httpsOrigin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "custom-dns-connect")
	}))
	defer httpsOrigin.Close()
	httpsURL := fixtureHostURL(t, httpsOrigin.URL, "origin-connect.fixture.test")
	resp, err = client.Get(httpsURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "custom-dns-connect" {
		t.Fatalf("CONNECT status=%s body=%q", resp.Status, body)
	}
	if got := queries.Load(); got < 4 {
		t.Fatalf("DNS fixture queries=%d, want A/AAAA lookups for both HTTP and CONNECT", got)
	}
}

func TestAPISS0031CustomDNSCoversUpstreamProxyHostname(t *testing.T) {
	dnsAddr, queries, stopDNS := startProxyDNSFixture(t, false)
	defer stopDNS()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			t.Errorf("upstream request URL is not absolute: %q", r.URL.String())
		}
		_, _ = io.WriteString(w, "via-custom-dns-upstream")
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server = "http://upstream.fixture.test:" + port
	cfg.DNS = "udp://" + dnsAddr
	px := startTestProxy(t, cfg)
	client := proxyClient(t, px.Port())
	resp, err := client.Get("http://target-never-resolved.fixture.test/resource")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "via-custom-dns-upstream" {
		t.Fatalf("status=%s body=%q", resp.Status, body)
	}
	if got := queries.Load(); got < 2 {
		t.Fatalf("DNS fixture queries=%d, upstream hostname was not resolved through policy", got)
	}
}

func TestAPISS0031CustomDNSCoversRemotePACFetch(t *testing.T) {
	dnsAddr, queries, stopDNS := startProxyDNSFixture(t, false)
	defer stopDNS()
	pacServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		_, _ = io.WriteString(w, `function FindProxyForURL(url, host) { return "DIRECT"; }`)
	}))
	defer pacServer.Close()
	pacURL := fixtureHostURL(t, pacServer.URL+"/proxy.pac", "pac.fixture.test")

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "remote-pac-custom-dns")
	}))
	defer origin.Close()
	originURL := fixtureHostURL(t, origin.URL, "pac-target.fixture.test")

	cfg := config.Default()
	cfg.PAC = pacURL
	cfg.DNS = "udp://" + dnsAddr
	px := startTestProxy(t, cfg)
	client := proxyClient(t, px.Port())
	resp, err := client.Get(originURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "remote-pac-custom-dns" {
		t.Fatalf("status=%s body=%q", resp.Status, body)
	}
	if got := queries.Load(); got < 4 {
		t.Fatalf("DNS fixture queries=%d, want PAC fetch plus target lookups", got)
	}
}

func TestAPISS0031CustomDNSCoversPACDNSResolve(t *testing.T) {
	dnsAddr, queries, stopDNS := startProxyDNSFixture(t, false)
	defer stopDNS()
	pacPath := t.TempDir() + "/resolver.pac"
	pacScript := `function FindProxyForURL(url, host) {
  return dnsResolve("pac-helper.fixture.test") == "127.0.0.1" ? "DIRECT" : "PROXY 127.0.0.1:1";
}`
	if err := os.WriteFile(pacPath, []byte(pacScript), 0o600); err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pac-dnsresolve-custom-dns")
	}))
	defer origin.Close()

	cfg := config.Default()
	cfg.PAC = pacPath
	cfg.DNS = "udp://" + dnsAddr
	px := startTestProxy(t, cfg)
	client := proxyClient(t, px.Port())
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "pac-dnsresolve-custom-dns" {
		t.Fatalf("status=%s body=%q", resp.Status, body)
	}
	if got := queries.Load(); got < 2 {
		t.Fatalf("DNS fixture queries=%d, PAC dnsResolve did not use custom resolver", got)
	}
}

func TestAPISS0031CustomDNSCoversNoProxyAddressMatch(t *testing.T) {
	dnsAddr, queries, stopDNS := startProxyDNSFixture(t, false)
	defer stopDNS()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		http.Error(w, "unexpected upstream", http.StatusBadGateway)
	}))
	defer upstream.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "noproxy-custom-dns")
	}))
	defer origin.Close()
	originURL := fixtureHostURL(t, origin.URL, "noproxy.fixture.test")

	cfg := config.Default()
	cfg.Server = upstream.URL
	cfg.NoProxy = "127.0.0.0/8"
	cfg.DNS = "udp://" + dnsAddr
	px := startTestProxy(t, cfg)
	client := proxyClient(t, px.Port())
	resp, err := client.Get(originURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "noproxy-custom-dns" {
		t.Fatalf("status=%s body=%q", resp.Status, body)
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream proxy calls=%d; noproxy address match did not bypass", got)
	}
	if got := queries.Load(); got < 2 {
		t.Fatalf("DNS fixture queries=%d, noproxy address match did not use custom resolver", got)
	}
}

func TestAPISS0031ConfiguredDNSDoesNotFallbackToSystemResolver(t *testing.T) {
	dnsAddr, _, stopDNS := startProxyDNSFixture(t, true)
	defer stopDNS()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "must-not-reach")
	}))
	defer origin.Close()
	u, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server = "DIRECT"
	cfg.DNS = "udp://" + dnsAddr
	px := startTestProxy(t, cfg)
	client := proxyClient(t, px.Port())
	resp, err := client.Get("http://localhost:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%s body=%q; system fallback appears possible", resp.Status, body)
	}
}

func TestAPISS0031DoctorReportsSafeDNSPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Server = "DIRECT"
	cfg.DNS = "https://resolver.example/dns-query"
	cfg.Sources["dns"] = "env:PXGO_DNS"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := s.DiagnosticSnapshot().DNS
	if status.Mode != "doh" || status.Source != "env" || status.Bootstrap != "system" {
		t.Fatalf("DNS diagnostic=%+v", status)
	}
	if len(status.Endpoints) != 1 || status.Endpoints[0] != "https://resolver.example" {
		t.Fatalf("safe DNS endpoint=%v", status.Endpoints)
	}
	if strings.Contains(strings.Join(status.Endpoints, ","), "dns-query") {
		t.Fatalf("DNS diagnostic leaked endpoint path: %v", status.Endpoints)
	}
}

func fixtureHostURL(t *testing.T, rawURL, hostname string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = net.JoinHostPort(hostname, port)
	return u.String()
}

func startProxyDNSFixture(t *testing.T, nameError bool) (string, *atomic.Int64, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64<<10)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			queries.Add(1)
			response, err := proxyDNSFixtureResponse(buf[:n], nameError)
			if err == nil {
				_, _ = pc.WriteTo(response, addr)
			}
		}
	}()
	return pc.LocalAddr().String(), &queries, func() {
		_ = pc.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("DNS fixture did not stop")
		}
	}
}

func proxyDNSFixtureResponse(query []byte, nameError bool) ([]byte, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(query)
	if err != nil {
		return nil, err
	}
	questions, err := parser.AllQuestions()
	if err != nil || len(questions) != 1 {
		return nil, fmt.Errorf("questions=%d err=%v", len(questions), err)
	}
	q := questions[0]
	rcode := dnsmessage.RCodeSuccess
	if nameError {
		rcode = dnsmessage.RCodeNameError
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		RecursionDesired:   header.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rcode,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(q); err != nil {
		return nil, err
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	if !nameError && q.Type == dnsmessage.TypeA {
		if err := builder.AResource(
			dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 30},
			dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}},
		); err != nil {
			return nil, err
		}
	}
	response, err := builder.Finish()
	if err != nil {
		return nil, err
	}
	if len(response) >= 2 && binary.BigEndian.Uint16(response[:2]) != header.ID {
		return nil, fmt.Errorf("response id mismatch")
	}
	return response, nil
}
