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
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// TestStrictLongIntakeAllProvisionalSlowHeadersTimesOut proves conditional
// short progress when untrusted accepted TCP clients continuously trickle
// *incomplete* HTTP headers. net/http ReadHeaderTimeout must be an absolute
// deadline, not an idle timer prolonged by attacker-controlled bytes.
// This does NOT implement short priority, protected physical reservations,
// or fairness under new provisional connections replacing expired ones.
func TestStrictLongIntakeAllProvisionalSlowHeadersTimesOut(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")

	cfg := config.Default()
	cfg.MaxConnections = 16
	cfg.ProxyReload = 0
	cfg.Idle = 6
	cfg.SockTimeout = 3
	px := startTestProxy(t, cfg)

	silent := make([]net.Conn, 0, 16)
	stopDrip := make(chan struct{})
	var writers sync.WaitGroup
	var dripWrites atomic.Int64
	defer func() {
		close(stopDrip)
		for _, c := range silent {
			if c != nil {
				_ = c.Close()
			}
		}
		writers.Wait()

		// Observe exact physical/class drain BEFORE shutdown; shutdown
		// must never be used to hide leaked physical permits.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			s := px.capacity.snapshot()
			if s.ActiveDownstream == 0 && s.ClassPools != nil && s.ClassPools.Total == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		s := px.capacity.snapshot()
		if s.ActiveDownstream != 0 || s.ClassPools == nil || s.ClassPools.Total != 0 {
			t.Errorf("physical/class permits leaked before shutdown: %+v", s)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := px.Shutdown(ctx); err != nil {
			t.Errorf("shutdown after slow-header timeout: %v", err)
		}
	}()

	// All clients transmit an INCOMPLETE header, rather than remaining
	// fully idle (the R53 experiment). Even valid header prefix bytes must
	// not reset the HTTP read-header deadline.
	for i := 0; i < 16; i++ {
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 2*time.Second)
		if err != nil {
			t.Fatalf("partial header[%d] dial failed: %v", i, err)
		}
		silent = append(silent, c)
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: slow.invalid\r\nX-Slow: "); err != nil {
			t.Fatalf("partial header[%d] prefix write failed: %v", i, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := px.capacity.snapshot()
		if s.ActiveDownstream == 16 && s.ClassPools != nil && s.ClassPools.Provisional == 16 &&
			s.ClassPools.Long == 0 && s.ClassPools.Short == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s := px.capacity.snapshot()
	if s.ActiveDownstream != 16 || s.ClassPools == nil || s.ClassPools.Provisional != 16 {
		t.Fatalf("not all partial-header clients owned provisional physical permits: %+v", s)
	}

	// Periodic genuine payload on 16 existing sockets; writers do not
	// voluntarily close/EOF. The server's own fixed read deadline must
	// close them. Every writer ends on stop or a native write error.
	for _, conn := range silent {
		writers.Add(1)
		go func(c net.Conn) {
			defer writers.Done()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopDrip:
					return
				case <-ticker.C:
					if _, err := io.WriteString(c, "x"); err != nil {
						return
					}
					dripWrites.Add(1)
				}
			}
		}(conn)
	}

	// Deliberately let earlier partial headers age so the short request
	// gets a later acquisition timer than the existing 16 TCP sessions.
	time.Sleep(750 * time.Millisecond)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "recovered-after-trickling-header-timeout")
	}))
	defer origin.Close()
	type outcome struct {
		status int
		body   string
		err    error
	}
	before := px.capacity.snapshot().AdmissionWaits
	done := make(chan outcome, 1)
	started := time.Now()
	go func() {
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 3*time.Second)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(6 * time.Second))
		if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
			origin.URL, origin.Listener.Addr()); err != nil {
			done <- outcome{err: err}
			return
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodGet})
		if err != nil {
			done <- outcome{err: err}
			return
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		done <- outcome{status: resp.StatusCode, body: string(body), err: err}
	}()

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if px.capacity.snapshot().AdmissionWaits > before {
			break
		}
		select {
		case got := <-done:
			t.Fatalf("short completed without any physical admission wait: %+v", got)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if px.capacity.snapshot().AdmissionWaits <= before {
		t.Fatal("short did not enter actual physical capacity admission wait")
	}
	select {
	case got := <-done:
		t.Fatalf("short unexpectedly completed while 16 provisional leases held: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case got := <-done:
		if got.err != nil || got.status != http.StatusOK ||
			!strings.Contains(got.body, "recovered-after-trickling-header-timeout") {
			t.Fatalf("short did not recover after real partial-header deadline: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("short remained blocked despite absolute ReadHeaderTimeout")
	}
	elapsed := time.Since(started)
	if dripWrites.Load() < 16 {
		t.Fatalf("insufficient genuine incomplete-header drip traffic: successful_writes=%d", dripWrites.Load())
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("short did not actually wait for physical permit: %s", elapsed)
	}
	s = px.capacity.snapshot()
	if s.ClassPools == nil || s.ClassPools.Long != 0 || s.ClassPools.LongIntakeDenied != 0 {
		t.Fatalf("short recovery produced spurious long-intake rejection: %+v", s)
	}
	t.Logf("STRICT_PROVISIONAL_TRICKLE_RESULT initial_provisional=16 physical_cap=16 read_header_timeout_s=3 drip_writes=%d short_wait_proven=true short_recovered_without_manual_close=true elapsed=%s",
		dripWrites.Load(), elapsed)
}
