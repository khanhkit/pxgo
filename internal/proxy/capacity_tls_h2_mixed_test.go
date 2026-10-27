package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Opt-in, real-socket admission regression. Multiplexed TLS 1.3/HTTP2/SSE
// streams share ONE physical CONNECT permit per TLS transport; independent
// short HTTP/1.1 connections compete under the same manual hard ceiling.
// This is NOT an AD/SSPI, WAN soak or class-aware fairness proof.
//
//nolint:gocyclo // Single test-only real-socket orchestration keeps all failure/cleanup invariants visible.
func TestCapacityTLSHTTP2StreamingMixed(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_TLS_H2_BENCH") != "1" {
		t.Skip("opt-in TLS/HTTP2 streaming capacity research")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("test requires experimental AUTO mode")
	}
	const h2Connections, heldEcho, borrowFill, shortRequests, h2Requests, hardCap = 6, 6, 4, 64, 48, 16
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	tlsOrigin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.TLS.Version != tls.VersionTLS13 || r.ProtoMajor != 2 {
			http.Error(w, "TLS 1.3 + HTTP/2 required", http.StatusHTTPVersionNotSupported)
			return
		}
		switch r.URL.Path {
		case "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: ready\n\n")
			f, ok := w.(http.Flusher)
			if !ok {
				return
			}
			f.Flush()
			select {
			case <-release:
				_, _ = io.WriteString(w, "data: done\n\n")
				f.Flush()
			case <-r.Context().Done():
			}
		case "/h2":
			_, _ = io.WriteString(w, "h2:"+r.URL.Query().Get("id"))
		default:
			http.NotFound(w, r)
		}
	}))
	tlsOrigin.EnableHTTP2 = true
	tlsOrigin.StartTLS()
	// On t.Fatal, release streaming handlers BEFORE httptest.Server.Close.
	defer func() { unblock(); tlsOrigin.Close() }()

	shortOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "mixed-ok")
	}))
	defer shortOrigin.Close()

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var acceptWG, streamWG sync.WaitGroup
	acceptWG.Add(1)
	go func() {
		defer acceptWG.Done()
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			streamWG.Add(1)
			go func(c net.Conn) {
				defer streamWG.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	held := make([]net.Conn, heldEcho+borrowFill)
	defer func() {
		for _, c := range held {
			if c != nil {
				_ = c.Close()
			}
		}
		_ = echo.Close()
		acceptWG.Wait()
		streamWG.Wait()
	}()

	cfg := config.Default()
	cfg.MaxConnections = hardCap
	cfg.ProxyReload = 0
	cfg.Idle = 40
	cfg.SockTimeout = 12
	px := startTestProxy(t, cfg)
	proxyAddr := px.ListenAddr()
	if px.capacity == nil {
		t.Fatal("AUTO capacity governor missing")
	}
	for i := 0; i < heldEcho; i++ {
		c, e := capacityMixedConnect(proxyAddr, echo.Addr().String(), i)
		if e != nil {
			t.Fatalf("CONNECT %d: %v", i, e)
		}
		held[i] = c
	}

	roots := x509.NewCertPool()
	roots.AddCert(tlsOrigin.Certificate())
	if len(tlsOrigin.Certificate().DNSNames) == 0 {
		t.Fatal("TLS test certificate has no DNS names")
	}
	tlsConfig := &tls.Config{
		RootCAs: roots, ServerName: tlsOrigin.Certificate().DNSNames[0],
		MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"},
	}
	clients := make([]*http.Client, h2Connections)
	transports := make([]*http.Transport, h2Connections)
	defer func() {
		for _, tr := range transports {
			if tr != nil {
				tr.CloseIdleConnections()
			}
		}
	}()
	for i := range clients {
		tr := &http.Transport{
			ForceAttemptHTTP2: true,
			MaxConnsPerHost:   1,
			TLSClientConfig:   tlsConfig,
			DialTLSContext: func(ctx context.Context, _, target string) (net.Conn, error) {
				c, e := (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "tcp", proxyAddr)
				if e != nil {
					return nil, e
				}
				ok := false
				defer func() {
					if !ok {
						_ = c.Close()
					}
				}()
				if e = c.SetDeadline(time.Now().Add(10 * time.Second)); e != nil {
					return nil, e
				}
				if _, e = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); e != nil {
					return nil, e
				}
				response, e := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
				if e != nil {
					return nil, e
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK {
					return nil, fmt.Errorf("CONNECT failed: %s", response.Status)
				}
				secure := tls.Client(c, tlsConfig.Clone())
				if e = secure.HandshakeContext(ctx); e != nil {
					return nil, e
				}
				if s := secure.ConnectionState(); s.NegotiatedProtocol != "h2" || s.Version != tls.VersionTLS13 {
					return nil, fmt.Errorf("negotiated %q TLS version %d, expected TLS 1.3 + h2", s.NegotiatedProtocol, s.Version)
				}
				if e = secure.SetDeadline(time.Time{}); e != nil {
					return nil, e
				}
				ok = true
				return secure, nil
			},
		}
		transports[i] = tr
		clients[i] = &http.Client{Transport: tr, Timeout: 15 * time.Second}
	}
	type streamResult struct{ err error }
	ready := make(chan error, h2Connections)
	finished := make(chan streamResult, h2Connections)
	for i, client := range clients {
		go func(i int, client *http.Client) {
			resp, e := client.Get(tlsOrigin.URL + "/sse?stream=" + strconv.Itoa(i))
			if e != nil {
				ready <- e
				finished <- streamResult{err: e}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				e = fmt.Errorf("SSE[%d] status=%s proto=%s content-type=%q", i, resp.Status, resp.Proto, resp.Header.Get("Content-Type"))
				ready <- e
				finished <- streamResult{err: e}
				return
			}
			reader := bufio.NewReader(resp.Body)
			first, e := reader.ReadString('\n')
			if e == nil && first != "data: ready\n" {
				e = fmt.Errorf("SSE[%d] first event %q", i, first)
			}
			if e == nil {
				var blank string
				blank, e = reader.ReadString('\n')
				if e == nil && blank != "\n" {
					e = fmt.Errorf("SSE[%d] framing %q", i, blank)
				}
			}
			ready <- e
			if e == nil {
				var rest []byte
				rest, e = io.ReadAll(reader)
				if e == nil && string(rest) != "data: done\n\n" {
					e = fmt.Errorf("SSE[%d] trailing event %q", i, rest)
				}
			}
			finished <- streamResult{err: e}
		}(i, client)
	}
	for i := 0; i < h2Connections; i++ {
		if e := <-ready; e != nil {
			t.Fatalf("SSE readiness[%d]: %v", i, e)
		}
	}
	snapshot := px.capacityDiagnosticSnapshot()
	if snapshot.ActiveDownstream != heldEcho+h2Connections || snapshot.EffectiveLimit != hardCap {
		t.Fatalf("physical admission/ceiling before load: %+v", snapshot)
	}
	if snapshot.ClassPools != nil && (snapshot.ClassPools.Long != heldEcho+h2Connections || snapshot.ClassPools.Short != 0 || snapshot.ClassPools.Provisional != 0 || snapshot.ClassPools.Total != heldEcho+h2Connections) {
		t.Fatalf("initial long class conservation: %+v", snapshot.ClassPools)
	}

	errs := make(chan error, shortRequests+h2Requests)
	var load sync.WaitGroup
	shortTokens := make(chan struct{}, 4)
	for i := 0; i < shortRequests; i++ {
		load.Add(1)
		go func(i int) {
			defer load.Done()
			shortTokens <- struct{}{}
			defer func() { <-shortTokens }()
			c, e := net.DialTimeout("tcp", proxyAddr, 4*time.Second)
			if e == nil {
				e = capacityMixedHTTP(c, bufio.NewReader(c), shortOrigin.URL, false)
				_ = c.Close()
			}
			if e != nil {
				errs <- fmt.Errorf("short[%d]: %w", i, e)
			}
		}(i)
	}
	for i := 0; i < h2Requests; i++ {
		load.Add(1)
		go func(i int) {
			defer load.Done()
			resp, e := clients[i%h2Connections].Get(tlsOrigin.URL + "/h2?id=" + strconv.Itoa(i))
			if e != nil {
				errs <- fmt.Errorf("HTTP2[%d]: %w", i, e)
				return
			}
			payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 128))
			_ = resp.Body.Close()
			if readErr != nil || resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 || string(payload) != "h2:"+strconv.Itoa(i) {
				errs <- fmt.Errorf("HTTP2[%d] status=%s proto=%s body=%q readErr=%v", i, resp.Status, resp.Proto, payload, readErr)
			}
		}(i)
	}
	load.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	for i := 0; i < heldEcho; i++ {
		if e := verifyCapacityHeldTunnel(held[i], i); e != nil {
			t.Errorf("held tunnel[%d] after TLS/H2 load: %v", i, e)
		}
	}
	// Fill every remaining physical permit while six TLS/H2 SSE streams
	// stay open. One independent short request MUST block until a long
	// CONNECT explicitly releases its sole physical permit.
	for i := heldEcho; i < heldEcho+borrowFill; i++ {
		c, e := capacityMixedConnect(proxyAddr, echo.Addr().String(), i)
		if e != nil {
			t.Fatalf("saturating CONNECT[%d]: %v", i, e)
		}
		held[i] = c
	}
	snapshot = px.capacityDiagnosticSnapshot()
	if snapshot.ActiveDownstream != hardCap || snapshot.EffectiveLimit != hardCap {
		t.Fatalf("hard envelope not actually saturated: %+v", snapshot)
	}
	if snapshot.ClassPools != nil && (snapshot.ClassPools.Long != hardCap || snapshot.ClassPools.Total != hardCap || snapshot.ClassPools.Short != 0 || snapshot.ClassPools.Provisional != 0) {
		t.Fatalf("saturated class ledger mismatch: %+v", snapshot.ClassPools)
	}
	// R24 audit fix: a mere 150ms unfinished request may still be queued
	// in the kernel backlog. Prove this exact short has reached the proxy's
	// bounded global acquire() and registered an admission wait.
	waitsBefore := snapshot.AdmissionWaits
	borrow := make(chan error, 1)
	dialed := make(chan error, 1)
	go func() {
		c, e := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
		dialed <- e
		if e == nil {
			e = capacityMixedHTTP(c, bufio.NewReader(c), shortOrigin.URL, false)
			_ = c.Close()
		}
		borrow <- e
	}()
	select {
	case e := <-dialed:
		if e != nil {
			t.Fatalf("short TCP dial failed while global permits saturated: %v", e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("short TCP dial did not reach a listening proxy")
	}
	observedWait := false
	waitDeadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(waitDeadline) {
		current := px.capacityDiagnosticSnapshot()
		if current.ActiveDownstream == hardCap && current.AdmissionWaits > waitsBefore {
			observedWait = true
			break
		}
		select {
		case e := <-borrow:
			t.Fatalf("short finished before reaching global admission wait: %v", e)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observedWait {
		t.Fatal("unproven blocked short: no admission wait under 16/16 physical saturation")
	}
	select {
	case e := <-borrow:
		t.Fatalf("short request completed while all global permits held: %v", e)
	case <-time.After(150 * time.Millisecond):
		// Required blocked short already observed inside global acquire().
	}
	_ = held[heldEcho].Close()
	held[heldEcho] = nil
	select {
	case e := <-borrow:
		if e != nil {
			t.Fatalf("short could not borrow released physical permit: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("short was starved after a long CONNECT released capacity")
	}
	replenished, e := capacityMixedConnect(proxyAddr, echo.Addr().String(), heldEcho)
	if e != nil {
		t.Fatalf("released physical permit could not be replenished: %v", e)
	}
	held[heldEcho] = replenished
	snapshot = px.capacityDiagnosticSnapshot()
	if snapshot.ActiveDownstream != hardCap {
		t.Fatalf("replenished physical permits: %+v", snapshot)
	}
	for i := heldEcho; i < len(held); i++ {
		_ = held[i].Close()
		held[i] = nil
	}
	for i := 0; i < heldEcho; i++ {
		if e := verifyCapacityHeldTunnel(held[i], i); e != nil {
			t.Errorf("long tunnel[%d] lost payload after physical borrow: %v", i, e)
		}
	}
	unblock()
	for i := 0; i < h2Connections; i++ {
		if e := (<-finished).err; e != nil {
			t.Errorf("SSE completion[%d]: %v", i, e)
		}
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		snapshot = px.capacityDiagnosticSnapshot()
		if snapshot.ActiveDownstream == heldEcho+h2Connections && (snapshot.ClassPools == nil || (snapshot.ClassPools.Long == heldEcho+h2Connections && snapshot.ClassPools.Short == 0 && snapshot.ClassPools.Provisional == 0 && snapshot.ClassPools.Total == heldEcho+h2Connections)) {
			t.Logf("TLS_H2_RESULT tls=tls13 alpn=h2 admission_wait_proven=true wait_delta=%d sse=%d h2_requests=%d short_http=%d held_connect=%d physical=%d hard=%d classes=%t", snapshot.AdmissionWaits-waitsBefore, h2Connections, h2Requests, shortRequests, heldEcho, snapshot.ActiveDownstream, snapshot.EffectiveLimit, snapshot.ClassPools != nil)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("TLS/H2/short connection conservation after load: %+v", snapshot)
}
