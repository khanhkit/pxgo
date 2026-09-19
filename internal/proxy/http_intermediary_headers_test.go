package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
)

func TestAbsoluteFormAuthorityCanonicalizesOutboundHost(t *testing.T) {
	seenHost := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost <- r.Host
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)

	px := startTestProxy(t, config.Default())
	conn, reader := rawIntermediaryProxyRequest(t, px, fmt.Sprintf("GET %s/authority HTTP/1.1\r\nHost: attacker.invalid\r\nConnection: close\r\n\r\n", origin.URL))
	defer conn.Close()
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := <-seenHost; got != originURL.Host {
		t.Fatalf("outbound Host=%q want request-target authority %q", got, originURL.Host)
	}
}

func TestOutboundRequestCanonicalizesProgrammaticHostMismatch(t *testing.T) {
	s := &Server{cfg: config.Default()}
	req := httptest.NewRequest(http.MethodGet, "http://origin.example.test/resource", nil)
	req.Host = "attacker.invalid"
	u, _ := url.Parse("http://origin.example.test/resource")
	out, err := s.newOutboundRequest(req, u, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Host != u.Host {
		t.Fatalf("outbound Host=%q want canonical authority %q", out.Host, u.Host)
	}
}

func TestOutboundRequestStripsHopByHopAndConnectionNominatedHeaders(t *testing.T) {
	s := &Server{cfg: config.Default()}
	req := httptest.NewRequest(http.MethodGet, "http://origin.example.test/resource", nil)
	req.Header.Set(headerConnection, "X-Hop, Keep-Alive")
	req.Header.Set("X-Hop", "secret")
	req.Header.Set(headerKeepAlive, "timeout=5")
	req.Header.Set("Proxy-Connection", "keep-alive")
	req.Header.Set("Proxy-Authorization", "Basic client-secret")
	req.Header.Set("TE", "gzip")
	req.Header.Set("Upgrade", "websocket")
	u, _ := url.Parse("http://origin.example.test/resource")
	out, err := s.newOutboundRequest(req, u, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{headerConnection, "X-Hop", headerKeepAlive, "Proxy-Connection", "Proxy-Authorization", "TE", "Upgrade"} {
		if got := out.Header.Get(name); got != "" {
			t.Fatalf("hop-by-hop request header %s leaked: %q", name, got)
		}
	}
}

func TestResponseStripsHopByHopAndConnectionNominatedHeaders(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerConnection, "X-Resp-Hop")
		w.Header().Set("X-Resp-Hop", "secret")
		w.Header().Set(headerKeepAlive, "timeout=5")
		w.Header().Set("Proxy-Authenticate", "Basic")
		w.Header().Set("X-End-To-End", "keep")
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	px := startTestProxy(t, config.Default())
	resp, err := proxyClient(t, px.Port()).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	for _, name := range []string{headerConnection, "X-Resp-Hop", headerKeepAlive, "Proxy-Authenticate"} {
		if got := resp.Header.Get(name); got != "" {
			t.Fatalf("hop-by-hop response header %s leaked: %q", name, got)
		}
	}
	if got := resp.Header.Get("X-End-To-End"); got != "keep" {
		t.Fatalf("end-to-end response header lost: %q", got)
	}
}

func TestForwardProxyPreservesCompressedRepresentation(t *testing.T) {
	seenAcceptEncoding := make(chan string, 1)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = zw.Write([]byte("compressed-payload"))
	_ = zw.Close()
	want := append([]byte(nil), compressed.Bytes()...)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAcceptEncoding <- r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(len(want)))
		_, _ = w.Write(want)
	}))
	defer origin.Close()

	px := startTestProxy(t, config.Default())
	conn, reader := rawIntermediaryProxyRequest(t, px, fmt.Sprintf("GET %s HTTP/1.1\r\nHost: ignored.example\r\nConnection: close\r\n\r\n", origin.URL))
	defer conn.Close()
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if enc := <-seenAcceptEncoding; enc != "" {
		t.Fatalf("proxy synthesized Accept-Encoding=%q", enc)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding mutated: %#v", resp.Header)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("representation bytes mutated: got=%d want=%d", len(got), len(want))
	}
}

func TestForwardProxyAddsViaBothDirections(t *testing.T) {
	seenVia := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenVia <- strings.Join(r.Header.Values("Via"), ", ")
		w.Header().Set("Via", "1.0 origin-gateway")
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	px := startTestProxy(t, config.Default())
	req, _ := http.NewRequest(http.MethodGet, origin.URL, nil)
	req.Header.Set("Via", "1.0 client-proxy")
	resp, err := proxyClient(t, px.Port()).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := <-seenVia; !strings.Contains(got, "1.0 client-proxy") || !strings.Contains(strings.ToLower(got), "pxgo") {
		t.Fatalf("request Via chain=%q", got)
	}
	got := strings.Join(resp.Header.Values("Via"), ", ")
	if !strings.Contains(got, "1.0 origin-gateway") || !strings.Contains(strings.ToLower(got), "pxgo") {
		t.Fatalf("response Via chain=%q", got)
	}
}

func rawIntermediaryProxyRequest(t *testing.T, px *Server, raw string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", px.ListenAddr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, raw); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}
