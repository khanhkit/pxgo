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
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// TestStrictLongIntakeAllProvisionalTimeout releases physical resources only
// through the real net/http ReadHeaderTimeout. It proves both the absence
// of a provisional-class admission quota in current AUTO and the conditional
// progress of a waiting short once idle unclassified TCP sockets expire.
// This is a test of existing behavior, not a new scheduler implementation.
func TestStrictLongIntakeAllProvisionalTimeout(t *testing.T) {
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
	defer func() {
		for _, c := range silent {
			if c != nil {
				_ = c.Close()
			}
		}
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
			t.Errorf("physical/class leases leaked before shutdown: %+v", s)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := px.Shutdown(ctx); err != nil {
			t.Errorf("shutdown after 16 idle provisional sockets: %v", err)
		}
	}()

	for i := 0; i < 16; i++ {
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 2*time.Second)
		if err != nil {
			t.Fatalf("idle provisional[%d] failed: %v", i, err)
		}
		silent = append(silent, c)
	}
	deadline := time.Now().Add(2 * time.Second)
	var admitted bool
	for time.Now().Before(deadline) {
		s := px.capacity.snapshot()
		if s.ActiveDownstream == 16 && s.ClassPools != nil &&
			s.ClassPools.Provisional == 16 && s.ClassPools.Long == 0 {
			admitted = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !admitted {
		t.Fatalf("16 silent provisional sockets were not all physically admitted: %+v", px.capacity.snapshot())
	}
	// Begin later than the header deadlines to give the *waiting* connection
	// a larger time budget than the 16 already-idle ones.
	time.Sleep(750 * time.Millisecond)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "recovered-after-real-header-timeout")
	}))
	defer origin.Close()

	type shortResult struct {
		status int
		body   string
		err    error
	}
	before := px.capacity.snapshot().AdmissionWaits
	done := make(chan shortResult, 1)
	started := time.Now()
	go func() {
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 3*time.Second)
		if err != nil {
			done <- shortResult{err: err}
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(6 * time.Second))
		if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
			origin.URL, origin.Listener.Addr()); err != nil {
			done <- shortResult{err: err}
			return
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodGet})
		if err != nil {
			done <- shortResult{err: err}
			return
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		done <- shortResult{status: resp.StatusCode, body: string(data), err: err}
	}()

	waitDeadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(waitDeadline) {
		if px.capacity.snapshot().AdmissionWaits > before {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("short completed before admission waiter proved: %+v", result)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if px.capacity.snapshot().AdmissionWaits <= before {
		t.Fatal("short did not enter real physical admission waiter")
	}
	select {
	case result := <-done:
		t.Fatalf("short unexpectedly completed while all 16 sockets idle: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case got := <-done:
		if got.err != nil || got.status != http.StatusOK ||
			!strings.Contains(got.body, "recovered-after-real-header-timeout") {
			t.Fatalf("short did not complete after silent TCP read-header deadline: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("short remained stuck despite finite ReadHeaderTimeout")
	}

	elapsed := time.Since(started)
	if elapsed < 150*time.Millisecond {
		t.Fatalf("short claimed liveness with no demonstrated physical admission delay: %s", elapsed)
	}
	snapshot := px.capacity.snapshot()
	if snapshot.ClassPools == nil || snapshot.ClassPools.Long != 0 ||
		snapshot.ClassPools.LongIntakeDenied != 0 {
		t.Fatalf("ordinary short inherited long quota error: %+v", snapshot)
	}
	t.Logf("STRICT_PROVISIONAL_TIMEOUT_RESULT initial_provisional=16 physical_cap=16 header_timeout_s=3 short_wait_proven=true short_completed_after_idle_expiry=true elapsed=%s", elapsed)
}
