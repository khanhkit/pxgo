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
	if got := m.snapshot(); got.Long != 8 || got.Promoted != 8 || got.LongOverflow != 5 || got.LongDebt != 5 || got.Total != 8 {
		t.Fatalf("debt/migration wrong: %+v", got)
	}
	for _, l := range leases {
		l.finish()
	}
	if got := m.snapshot(); got.Total != 0 || got.Long != 0 || got.Short != 0 || got.LongDebt != 0 {
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

// A real listener can acquire a physical token before Shutdown closes the
// class registration. This deterministic fixture forces that exact order.
type classesOnceListener struct {
	connection net.Conn
	returned   bool
}

func (l *classesOnceListener) Accept() (net.Conn, error) {
	if l.returned {
		return nil, net.ErrClosed
	}
	l.returned = true
	return l.connection, nil
}
func (l *classesOnceListener) Close() error   { return l.connection.Close() }
func (l *classesOnceListener) Addr() net.Addr { return l.connection.LocalAddr() }

func TestConnectionClassRegistrationClosedAfterPhysicalAcquire(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	g := newCapacityGovernor(modeAuto, config.Default())
	if g.classes == nil {
		t.Fatal("classifier not opted in")
	}
	server, peer := net.Pipe()
	defer peer.Close()
	ln := newDynamicAdmissionListener(&classesOnceListener{connection: server}, g, time.Second)
	g.classes.close() // global governor could still have a stale open window
	accepted, err := ln.Accept()
	if accepted != nil || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("shutdown registration race wrongly admitted %v: %v", accepted, err)
	}
	if got := g.snapshot(); got.ActiveDownstream != 0 || got.ClassPools == nil || got.ClassPools.Total != 0 {
		t.Fatalf("physical/class permit leaked on shutdown race: %+v", got)
	}
}

// AUTO class budgets are only telemetry. They must reflect the actual
// physical admission window, including explicit/manual bounds, shrinking
// hard OS envelopes and emergency pressure. They never admit extra sockets.
func TestConnectionClassBudgetsFollowEffectiveLimit(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	cfg := config.Default()
	cfg.MaxConnections = 16
	g := newCapacityGovernor(modeAuto, cfg)
	if g == nil || g.classes == nil {
		t.Fatal("AUTO classes tracker not created")
	}
	defer g.close()
	defer g.classes.close()
	assertBudget := func(wantEffective, wantShort, wantLong int) {
		t.Helper()
		got := g.snapshot()
		cls := g.classes.snapshot()
		if got.EffectiveLimit != wantEffective || cls.ShortBudget != wantShort || cls.LongBudget != wantLong || cls.ShortBudget+cls.LongBudget != wantEffective {
			t.Fatalf("effective and advisory class budgets diverged: governor=%+v classes=%+v; expected=%d/%d/%d", got, cls, wantEffective, wantShort, wantLong)
		}
	}
	assertBudget(16, 4, 12) // manual cap, not 256 initial soft target
	g.mu.Lock()
	g.hard = 7 // native OS or Job pressure shrinks usable physical slots
	g.recomputeLocked()
	g.mu.Unlock()
	assertBudget(7, 1, 6)
	g.mu.Lock()
	g.hard = 256
	g.soft = 40 // growth must still be constrained by manual 16
	g.recomputeLocked()
	g.mu.Unlock()
	assertBudget(16, 4, 12)
	g.mu.Lock()
	g.state = capacityStatePressure
	g.reason = capacityEmergencyFD
	g.probeAllowed = false
	g.recomputeLocked()
	g.mu.Unlock()
	assertBudget(0, 0, 0)
	g.mu.Lock()
	g.state = capacityStateNormal
	g.reason = "none"
	g.recomputeLocked()
	g.mu.Unlock()
	assertBudget(16, 4, 12)
}

func TestConnectionClassBudgetsDoNotEvictExistingLongOnShrink(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	cfg := config.Default()
	cfg.MaxConnections = 16
	g := newCapacityGovernor(modeAuto, cfg)
	defer g.close()
	defer g.classes.close()
	now := time.Unix(0, 0)
	leases := make([]*connectionClassLease, 0, 16)
	for i := 0; i < 16; i++ {
		l := g.classes.open(now)
		if l == nil {
			t.Fatal("nil class lease")
		}
		l.classifyRequest(&http.Request{Method: http.MethodConnect, Header: make(http.Header)}, now)
		leases = append(leases, l)
	}
	g.mu.Lock()
	g.active = 16
	g.hard = 8
	g.recomputeLocked()
	g.mu.Unlock()
	cls := g.classes.snapshot()
	if cls.Total != 16 || cls.Long != 16 || cls.LongBudget != 6 || cls.LongDebt != 10 || cls.ShortBudget != 2 {
		t.Fatalf("shrink must never eject existing established longs; got %+v", cls)
	}
	for _, lease := range leases {
		lease.finish()
	}
	if got := g.classes.snapshot(); got.Total != 0 || got.LongDebt != 0 {
		t.Fatalf("leases did not drain after cap shrink: %+v", got)
	}
}

func TestStrictLongIntakeClassGuardAndDebt(t *testing.T) {
	m := newConnectionClassManager()
	m.strictLongIntake = true
	m.sampleBudgets(16) // 12 long, four available for short/provisional
	now := time.Unix(1000, 0)
	connect := &http.Request{Method: http.MethodConnect, Header: make(http.Header)}
	shortReq := &http.Request{Method: http.MethodGet, Header: make(http.Header)}
	var longs []*connectionClassLease
	for i := 0; i < 12; i++ {
		l := m.open(now)
		if !l.classifyRequest(connect, now) {
			t.Fatalf("long %d wrongly denied within budget", i)
		}
		longs = append(longs, l)
	}
	denied := m.open(now)
	if denied.classifyRequest(connect, now) {
		t.Fatal("13th newly authenticated CONNECT bypassed long cap")
	}
	if s := m.snapshot(); s.Long != 12 || s.Provisional != 1 || s.LongDenied != 1 || s.LongDebt != 0 {
		t.Fatalf("denied long must retain provisional class until socket close: %+v", s)
	}
	short := m.open(now)
	if !short.classifyRequest(shortReq, now) || m.snapshot().Short != 1 {
		t.Fatalf("short request blocked despite reserved budget: %+v", m.snapshot())
	}
	longs[0].finish()
	if !denied.classifyRequest(connect, now) {
		t.Fatal("released long must allow one previously denied CONNECT")
	}
	if s := m.snapshot(); s.Long != 12 || s.Short != 1 || s.Provisional != 0 || s.LongDenied != 1 {
		t.Fatalf("new long after release did not conserve class tokens: %+v", s)
	}
	// Active long never gets evicted; aged short promotes without a second
	// global permit even if this creates long debt.
	m.promoteAged(now.Add(31 * time.Second))
	if s := m.snapshot(); s.Long != 13 || s.LongDebt != 1 || s.Short != 0 {
		t.Fatalf("automatic promotion lost or overbooked class debt: %+v", s)
	}
	another := m.open(now)
	if another.classifyRequest(connect, now) {
		t.Fatal("new CONNECT accepted while promoted long debt exists")
	}
	another.finish()
	denied.finish()
	short.finish()
	for _, l := range longs {
		l.finish()
	}
	if s := m.snapshot(); s.Total != 0 || s.LongDebt != 0 || s.LongDenied != 2 {
		t.Fatalf("class leases/debt leaked: %+v", s)
	}
}

func TestStrictLongIntakeServerModeValidation(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	cfg := config.Default()
	for _, mode := range []string{"legacy", "shadow", "auto"} {
		t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", mode)
		t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "0")
		s, err := New(cfg)
		if err == nil {
			_ = s.Shutdown(context.Background())
			t.Fatalf("%s silently accepted strict guard with class tracking OFF", mode)
		}
	}
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("properly opted-in strict long intake must initialize: %v", err)
	}
	if s.capacity == nil || s.capacity.classes == nil || !s.capacity.classes.strictLongIntake {
		t.Fatal("strict long intake missing with all required opt-ins")
	}
	_ = s.Shutdown(context.Background())
}

func TestStrictLongIntakeRealCONNECTAndShortHTTP(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	cfg := config.Default()
	cfg.MaxConnections = 16
	cfg.ProxyReload = 0
	cfg.Idle = 40
	px := startTestProxy(t, cfg)
	defer px.Shutdown(context.Background())
	if got := px.capacity.classes.snapshot(); got.LongBudget != 12 || got.ShortBudget != 4 {
		t.Fatalf("manual global physical cap 16 must reserve 4 short slots: %+v", got)
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	connect := func() (net.Conn, int) {
		t.Helper()
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n",
			echo.Addr().String(), echo.Addr().String()); err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
		response, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
		if err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return c, response.StatusCode
	}
	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < 12; i++ {
		conn, status := connect()
		held = append(held, conn)
		if status != http.StatusOK {
			t.Fatalf("long %d expected 200, got %d", i, status)
		}
	}
	if got := px.capacity.classes.snapshot(); got.Long != 12 {
		t.Fatalf("expected 12 held long leases: %+v", got)
	}
	rejectedConn, rejectedStatus := connect()
	_ = rejectedConn.Close()
	if rejectedStatus != http.StatusServiceUnavailable {
		t.Fatalf("13th CONNECT must return 503 while long capacity filled, got %d", rejectedStatus)
	}
	if stats := px.capacity.snapshot().ClassPools; stats == nil || stats.LongIntakeDenied != 1 {
		t.Fatalf("Doctor must expose one denied new long request: %+v", stats)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "allowed-short")
	}))
	defer origin.Close()
	client, err := net.DialTimeout("tcp", px.ListenAddr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(client, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", origin.URL, origin.Listener.Addr())
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(4 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	_ = client.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("short HTTP must still make progress while 12 long held: %s", resp.Status)
	}
	_ = held[0].Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if px.capacity.classes.snapshot().Long == 11 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := px.capacity.classes.snapshot(); got.Long != 11 {
		t.Fatalf("long close did not reclaim one advisory class slot: %+v", got)
	}
	fresh, status := connect()
	held = append(held, fresh)
	if status != http.StatusOK {
		t.Fatalf("released long slot must allow new CONNECT, got %d", status)
	}
}

func TestStrictLongIntakeWebSocketSSEAndConcurrentLimit(t *testing.T) {
	m := newConnectionClassManager()
	m.strictLongIntake = true
	m.sampleBudgets(8) // long cap=6
	now := time.Unix(2000, 0)
	connect := &http.Request{Method: http.MethodConnect, Header: make(http.Header)}
	var longs []*connectionClassLease
	for i := 0; i < 6; i++ {
		l := m.open(now)
		if !l.classifyRequest(connect, now) {
			t.Fatalf("long lease %d unexpectedly denied", i)
		}
		longs = append(longs, l)
	}
	for _, req := range []*http.Request{
		{Method: http.MethodGet, Header: http.Header{headerUpgrade: []string{"websocket"}}},
		{Method: http.MethodGet, Header: http.Header{"Accept": []string{"text/event-stream"}}},
	} {
		l := m.open(now)
		if l.classifyRequest(req, now) {
			t.Fatalf("new WS/SSE long bypassed full long budget: %v", req.Header)
		}
		l.finish()
	}
	if got := m.snapshot(); got.LongDenied != 2 || got.Long != 6 {
		t.Fatalf("WS/SSE rejection did not conserve class pool: %+v", got)
	}
	for _, l := range longs {
		l.finish()
	}
	m = newConnectionClassManager()
	m.strictLongIntake = true
	m.sampleBudgets(16)
	const attempts = 100
	var wg sync.WaitGroup
	var leasesMu sync.Mutex
	var leases []*connectionClassLease
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := m.open(now)
			_ = l.classifyRequest(connect, now)
			leasesMu.Lock()
			leases = append(leases, l)
			leasesMu.Unlock()
		}()
	}
	wg.Wait()
	if got := m.snapshot(); got.Long != 12 || got.LongDenied != attempts-12 || got.LongDebt != 0 || got.Total != attempts {
		t.Fatalf("concurrent strict guard not atomic: %+v", got)
	}
	for _, l := range leases {
		l.finish()
	}
	if got := m.snapshot(); got.Total != 0 || got.Provisional != 0 {
		t.Fatalf("concurrent lease cleanup failed: %+v", got)
	}
}

func TestStrictLongIntakeFailsClosedWithoutOwnedClassLease(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	s, err := New(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	// A non-ConnContext path must never bypass a strict experimental guard.
	// This test calls ServeHTTP directly, so no owned physical lease exists.
	req := httptest.NewRequest(http.MethodConnect, "http://localhost:12345", nil)
	req.RemoteAddr = "127.0.0.1:56789"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing authenticated classification lease must fail closed in strict mode, got %d", w.Code)
	}
	if stats := s.capacity.snapshot().ClassPools; stats == nil || stats.Long != 0 || stats.Total != 0 {
		t.Fatalf("rejected unowned request changed class permit state: %+v", stats)
	}
}

// A lowered physical cap must never evict existing authenticated longs.
// Provisional sockets that already own a physical token may still be
// classified, but fresh explicit long upgrades must honor the new budget.
func TestStrictLongIntakeBudgetShrinkOutstandingProvisional(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	cfg := config.Default()
	cfg.MaxConnections = 16
	g := newCapacityGovernor(modeAuto, cfg)
	defer g.close()
	defer g.classes.close()
	now := time.Unix(10000, 0)
	longs := make([]*connectionClassLease, 12)
	for i := range longs {
		longs[i] = g.classes.open(now)
		if longs[i] == nil || !longs[i].classifyRequest(&http.Request{Method: http.MethodConnect}, now) {
			t.Fatalf("initial long[%d] should be admitted", i)
		}
	}
	provisional := make([]*connectionClassLease, 4)
	for i := range provisional {
		provisional[i] = g.classes.open(now)
		if provisional[i] == nil {
			t.Fatalf("initial provisional[%d] missing", i)
		}
	}
	g.mu.Lock()
	g.active = 16 // model the 16 already-acquired physical tokens
	g.hard = 8
	g.recomputeLocked()
	g.mu.Unlock()
	s := g.classes.snapshot()
	if s.Long != 12 || s.Provisional != 4 || s.LongBudget != 6 || s.ShortBudget != 2 || s.LongDebt != 6 {
		t.Fatalf("shrink incorrectly evicted or misbudgeted live sockets: %+v", s)
	}
	if provisional[0].classifyRequest(&http.Request{Method: http.MethodConnect}, now) {
		t.Fatal("fresh explicit long upgrade must not bypass shrunken quota")
	}
	provisional[0].finish() // HTTP 503 + close returns its one physical token
	if !provisional[1].classifyRequest(&http.Request{Method: http.MethodGet}, now) {
		t.Fatal("existing provisional socket may classify as ordinary short")
	}
	for i := 0; i < 8; i++ {
		longs[i].finish()
	}
	s = g.classes.snapshot()
	if s.Long != 4 || s.Short != 1 || s.Provisional != 2 || s.LongDebt != 0 || s.LongDenied != 1 {
		t.Fatalf("drained long budget/debt mismatch: %+v", s)
	}
	if !provisional[2].classifyRequest(&http.Request{Method: http.MethodConnect}, now) {
		t.Fatal("existing eligible provisional may upgrade after long debt drains")
	}
	s = g.classes.snapshot()
	if s.Long != 5 || s.LongBudget != 6 || s.LongDenied != 1 {
		t.Fatalf("long intake did not resume at effective budget: %+v", s)
	}
	for _, lease := range longs {
		lease.finish()
	}
	for _, lease := range provisional {
		lease.finish()
	}
	if s = g.classes.snapshot(); s.Total != 0 || s.Provisional != 0 || s.Short != 0 || s.Long != 0 || s.LongDebt != 0 {
		t.Fatalf("all preexisting leases must drain: %+v", s)
	}
}
