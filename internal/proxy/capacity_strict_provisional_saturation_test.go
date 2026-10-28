package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// TestStrictLongIntakeProvisionalSaturationDocumentsNoShortLiveness is a
// NEGATIVE behavioral contract: a post-authentication long-class budget is
// not a physical short reservation. Four unclassified TCP connections can
// hold the four "reserved" slots while twelve authenticated long tunnels
// occupy the rest. A new short request cannot run until a physical slot
// is actually released. No production scheduler behavior is changed.
func TestStrictLongIntakeProvisionalSaturationDocumentsNoShortLiveness(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	cfg := config.Default()
	cfg.MaxConnections = 16
	cfg.ProxyReload = 0
	cfg.Idle = 40
	px := startTestProxy(t, cfg)

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var echoWG sync.WaitGroup
	echoWG.Add(1)
	go func() {
		defer echoWG.Done()
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			echoWG.Add(1)
			go func(c net.Conn) {
				defer echoWG.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()

	held := make([]net.Conn, 0, 12)
	provisional := make([]net.Conn, 0, 4)
	defer func() {
		for _, c := range provisional {
			if c != nil {
				_ = c.Close()
			}
		}
		for _, c := range held {
			_ = c.Close()
		}
		// A failed assertion must still drain actual physical/class tokens.
		// Do not hide a leak behind shutdown tearing down the governor.
		deadline := time.Now().Add(3 * time.Second)
		var drained bool
		for time.Now().Before(deadline) {
			s := px.capacity.snapshot()
			if s.ActiveDownstream == 0 && s.ClassPools != nil && s.ClassPools.Total == 0 {
				drained = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !drained {
			t.Errorf("physical/class permits were not drained before shutdown: %+v", px.capacity.snapshot())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := px.Shutdown(ctx); err != nil {
			t.Errorf("proxy shutdown: %v", err)
		}
		_ = echo.Close()
		echoWG.Wait()
	}()

	openLong := func() net.Conn {
		t.Helper()
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr(), echo.Addr()); err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
		if err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			_ = c.Close()
			t.Fatalf("CONNECT status=%s", resp.Status)
		}
		_ = c.SetReadDeadline(time.Time{})
		return c
	}
	for i := 0; i < 12; i++ {
		held = append(held, openLong())
	}
	for i := 0; i < 4; i++ {
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 2*time.Second)
		if err != nil {
			t.Fatalf("provisional %d: %v", i, err)
		}
		provisional = append(provisional, c) // intentionally no headers: preauth slowloris
	}
	until := time.Now().Add(3 * time.Second)
	var before int
	for time.Now().Before(until) {
		s := px.capacity.snapshot()
		if s.ClassPools != nil && s.ActiveDownstream == 16 && s.ClassPools.Long == 12 && s.ClassPools.Provisional == 4 {
			before = int(s.AdmissionWaits)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := px.capacity.snapshot()
	if snap.ActiveDownstream != 16 || snap.ClassPools == nil ||
		snap.ClassPools.Long != 12 || snap.ClassPools.Provisional != 4 ||
		snap.ClassPools.LongSoftBudget != 12 || snap.ClassPools.ShortSoftBudget != 4 {
		t.Fatalf("authenticated long + preauth did not occupy all 16 physical slots: %+v", snap)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "short-after-release")
	}))
	defer origin.Close()

	type outcome struct {
		status int
		body   string
		err    error
	}
	shortDone := make(chan outcome, 1)
	go func() {
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 3*time.Second)
		if err != nil {
			shortDone <- outcome{err: err}
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
			origin.URL, origin.Listener.Addr()); err != nil {
			shortDone <- outcome{err: err}
			return
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodGet})
		if err != nil {
			shortDone <- outcome{err: err}
			return
		}
		b, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		shortDone <- outcome{status: resp.StatusCode, body: string(b), err: err}
	}()

	// Check actual admission waiter rather than merely a sleeping producer.
	until = time.Now().Add(2 * time.Second)
	waiting := false
	for time.Now().Before(until) {
		if int(px.capacity.snapshot().AdmissionWaits) > before {
			waiting = true
			break
		}
		select {
		case got := <-shortDone:
			t.Fatalf("HTTP short unexpectedly completed with all physical slots held: %+v", got)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("short request never entered actual physical acquire wait")
	}
	select {
	case got := <-shortDone:
		t.Fatalf("physical capacity exhausted but HTTP short proceeded: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}

	// Release one unclassified TCP slot, not an authenticated long tunnel.
	_ = provisional[0].Close()
	provisional[0] = nil
	select {
	case got := <-shortDone:
		if got.err != nil || got.status != http.StatusOK || !strings.Contains(got.body, "short-after-release") {
			t.Fatalf("HTTP short did not recover after provisional close: %+v", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("short HTTP did not complete after releasing provisional physical permit")
	}
	after := px.capacity.snapshot()
	if after.ClassPools == nil || after.ClassPools.Long != 12 || after.ClassPools.LongIntakeDenied != 0 {
		t.Fatalf("longs not conserved or guard spuriously denied short: %+v", after)
	}
	t.Logf("STRICT_LONG_PREAUTH_RESULT capacity=16 long_held=12 provisional_before=4 short_admission_wait_proven=true short_blocked_before_release=true short_succeeded_after_release=true long_unchanged=12")
}
