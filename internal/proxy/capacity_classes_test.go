package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestConnectionClassPredictionAndAtomicPromotion(t *testing.T) {
	m := newConnectionClassManager()
	m.sampleBudgets(256)
	now := time.Unix(1000, 0)
	a := m.open(now)
	b := m.open(now)
	c := m.open(now)
	if got := m.snapshot(); got.Total != 3 || got.Provisional != 3 || got.LongBudget != 192 || got.ShortBudget != 64 {
		t.Fatalf("initial pool budgets: %+v", got)
	}
	a.classifyRequest(&http.Request{Method: http.MethodGet, Header: make(http.Header)}, now)
	b.classifyRequest(&http.Request{Method: http.MethodConnect, Header: make(http.Header)}, now)
	c.classifyRequest(&http.Request{Method: http.MethodGet, Header: http.Header{"Accept": []string{"text/event-stream"}}}, now)
	if got := m.snapshot(); got.Total != 3 || got.Provisional != 0 || got.Short != 1 || got.Long != 2 {
		t.Fatalf("classification incorrect: %+v", got)
	}
	// A connection initially classified as short migrates one accounting token
	// without asking the global governor for another permit or changing total.
	m.promoteAged(now.Add(31 * time.Second))
	if got := m.snapshot(); got.Total != 3 || got.Short != 0 || got.Long != 3 || got.Promoted != 1 {
		t.Fatalf("short->long migration lost a permit: %+v", got)
	}
	a.classifyRequest(&http.Request{Method: http.MethodGet}, now.Add(35*time.Second))
	if got := m.snapshot(); got.Long != 3 || got.Short != 0 {
		t.Fatalf("classified long conn demoted: %+v", got)
	}
	b.finish()
	b.finish()
	a.finish()
	c.finish()
	if got := m.snapshot(); got.Total != 0 || got.Long != 0 || got.Short != 0 || got.Provisional != 0 {
		t.Fatalf("nonzero closed leases: %+v", got)
	}
}

func TestConnectionClassOverBudgetDoesNotDenyPromotion(t *testing.T) {
	m := newConnectionClassManager()
	m.sampleBudgets(4) // long soft budget=3
	now := time.Unix(1000, 0)
	leases := make([]*connectionClassLease, 8)
	for i := range leases {
		leases[i] = m.open(now)
		leases[i].classifyRequest(&http.Request{Method: http.MethodGet}, now)
	}
	m.promoteAged(now.Add(31 * time.Second))
	if got := m.snapshot(); got.Long != 8 || got.Promoted != 8 || got.LongOverflow != 5 || got.Total != 8 {
		t.Fatalf("debt/migration wrong: %+v", got)
	}
	for _, l := range leases {
		l.finish()
	}
	if got := m.snapshot(); got.Total != 0 || got.Long != 0 || got.Short != 0 {
		t.Fatal(got)
	}
}

func TestConnectionClassRepeatRequestsAndUpgrade(t *testing.T) {
	m := newConnectionClassManager()
	now := time.Unix(3000, 0)
	a := m.open(now)
	for i := 0; i < classRequestThreshold; i++ {
		a.classifyRequest(&http.Request{Method: http.MethodGet, Header: make(http.Header)}, now.Add(time.Second))
	}
	if a.kind != classLong || m.snapshot().Promoted != 1 {
		t.Fatalf("repeat traffic not reclassified: %+v", m.snapshot())
	}
	b := m.open(now)
	b.classifyRequest(&http.Request{Method: http.MethodGet, Header: http.Header{headerUpgrade: []string{"websocket"}}}, now)
	if b.kind != classLong {
		t.Fatal("websocket predictor failed")
	}
	a.finish()
	b.finish()
}

func TestConnectionClassConcurrentClosePromotion(t *testing.T) {
	m := newConnectionClassManager()
	m.sampleBudgets(256)
	now := time.Unix(1000, 0)
	const n = 1000
	leases := make([]*connectionClassLease, n)
	for i := range leases {
		leases[i] = m.open(now)
	}
	var wg sync.WaitGroup
	for _, l := range leases {
		wg.Add(1)
		go func(l *connectionClassLease) {
			defer wg.Done()
			l.classifyRequest(&http.Request{Method: http.MethodGet}, now)
			l.finish()
			l.finish()
		}(l)
	}
	wg.Add(1)
	go func() { defer wg.Done(); m.promoteAged(now.Add(time.Minute)) }()
	wg.Wait()
	if got := m.snapshot(); got.Total != 0 || got.Short != 0 || got.Long != 0 || got.Provisional != 0 {
		t.Fatalf("atomic pool counters leaked: %+v", got)
	}
	m.close()
	if l := m.open(time.Now()); l != nil {
		t.Fatal("new lease after close")
	}
}

func TestConnectionClassContext(t *testing.T) {
	m := newConnectionClassManager()
	l := m.open(time.Now())
	if got := connectionClassFromContext(context.Background()); got != nil {
		t.Fatal("non-owned request assigned a class")
	}
	ctx := context.WithValue(context.Background(), connectionClassContextKey{}, l)
	if got := connectionClassFromContext(ctx); got != l {
		t.Fatal("lease context identity lost")
	}
	l.finish()
}

func TestConnectionClassHTTPAndCONNECTLifecycle(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.Idle = 40
	px := startTestProxy(t, cfg)
	if px.capacity.classes == nil {
		t.Fatal("opt-in tracker missing")
	}
	conn, err := net.DialTimeout("tcp", px.ListenAddr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "GET http://127.0.0.1:1/path HTTP/1.1\r\nHost: 127.0.0.1:1\r\nConnection: keep-alive\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := px.capacity.classes.snapshot(); got.Short != 1 || got.Total != 1 {
		t.Fatalf("normal keepalive must start short: %+v", got)
	}
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			c, e := upstream.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	tunnel, err := net.DialTimeout("tcp", px.ListenAddr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	_, err = fmt.Fprintf(tunnel, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.Addr(), upstream.Addr())
	if err != nil {
		t.Fatal(err)
	}
	_ = tunnel.SetReadDeadline(time.Now().Add(3 * time.Second))
	connectResp, err := http.ReadResponse(bufio.NewReader(tunnel), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	defer connectResp.Body.Close()
	if connectResp.StatusCode != 200 {
		t.Fatalf("CONNECT returned %s", connectResp.Status)
	}
	if got := px.capacity.classes.snapshot(); got.Short != 1 || got.Long != 1 || got.Total != 2 {
		t.Fatalf("CONNECT must be predicted long without taking another permit: %+v", got)
	}
	px.capacity.classes.promoteAged(time.Now().Add(31 * time.Second))
	if got := px.capacity.classes.snapshot(); got.Long != 2 || got.Promoted != 1 || got.Total != 2 {
		t.Fatalf("HTTP short->long promotion wrong: %+v", got)
	}
	_ = tunnel.Close()
	_ = conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if px.capacity.classes.snapshot().Total == 0 && px.capacity.snapshot().ActiveDownstream == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("classifier/global permit leaked: %+v capacity=%+v", px.capacity.classes.snapshot(), px.capacity.snapshot())
}
