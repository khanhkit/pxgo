package proxy

import (
	"bufio"
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

func TestR70Real256PhysicalCapReclaimsFromSlowProvisionalForShortHTTP(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "short-reclaimed")
	}))
	defer origin.Close()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) { defer wg.Done(); defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.MaxConnections = 256
	cfg.SockTimeout = 7
	cfg.Idle = 20
	px := startTestProxy(t, cfg)
	var longs, slow []net.Conn
	defer func() {
		for _, c := range slow {
			_ = c.Close()
		}
		for _, c := range longs {
			_ = c.Close()
		}
		_ = echo.Close()
		wg.Wait()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			cap := px.capacity.snapshot()
			classes := px.capacity.classes.snapshot()
			if cap.ActiveDownstream == 0 && classes.Total == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("R70 connections leaked physical=%+v classes=%+v", px.capacity.snapshot(), px.capacity.classes.snapshot())
	}()
	for i := 0; i < 192; i++ {
		c, e := capacityMixedConnect(px.ListenAddr(), echo.Addr().String(), i)
		if e != nil {
			t.Fatalf("long[%d]=%v", i, e)
		}
		longs = append(longs, c)
	}
	for i := 0; i < 64; i++ {
		c, e := net.DialTimeout("tcp", px.ListenAddr(), 2*time.Second)
		if e != nil {
			t.Fatalf("slow[%d]=%v", i, e)
		}
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, e = fmt.Fprintf(c, "GET http://127.0.0.1:1/ HTTP/1.1\r\nHost: stall.test\r\nX-Slow: ")
		if e != nil {
			_ = c.Close()
			t.Fatalf("write slow[%d]=%v", i, e)
		}
		slow = append(slow, c)
	}
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		snap := px.capacity.snapshot()
		classes := px.capacity.classes.snapshot()
		if snap.ActiveDownstream == 256 && classes.Long == 192 && classes.Provisional == 64 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap, classes := px.capacity.snapshot(), px.capacity.classes.snapshot(); snap.ActiveDownstream != 256 || classes.Long != 192 || classes.Provisional != 64 || snap.EffectiveLimit != 256 {
		t.Fatalf("real saturated physical 192 long+64 provisional not observed: %+v %+v", snap, classes)
	}
	shortResult := make(chan error, 1)
	go func() {
		c, e := net.DialTimeout("tcp", px.ListenAddr(), 2*time.Second)
		if e != nil {
			shortResult <- e
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, e = fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", origin.URL, origin.Listener.Addr().String())
		if e != nil {
			shortResult <- e
			return
		}
		resp, e := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodGet})
		if e != nil {
			shortResult <- e
			return
		}
		body, re := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if re != nil || resp.StatusCode != 200 || string(body) != "short-reclaimed" {
			shortResult <- fmt.Errorf("short response %s body=%q err=%v", resp.Status, body, re)
			return
		}
		shortResult <- nil
	}()
	select {
	case e := <-shortResult:
		t.Fatalf("short served before physical capacity freed (err=%v)", e)
	case <-time.After(150 * time.Millisecond):
	}
	if snap := px.capacity.snapshot(); snap.ActiveDownstream != 256 {
		t.Fatalf("physical cap shifted before reclaim: %+v", snap)
	}
	_ = slow[0].Close()
	select {
	case e := <-shortResult:
		if e != nil {
			t.Fatalf("short after reclaim error: %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("short blocked despite one reclaimed physical slot: %+v", px.capacity.snapshot())
	}
	classes := px.capacity.classes.snapshot()
	if classes.Long != 192 {
		t.Fatalf("long tunnels evicted to serve a short: %+v", classes)
	}
	t.Logf("R70_RECLAIM_RESULT held_long=192 provisional_initial=64 physical_saturated=256 reclaimed_provisional=1 short_success=true long_preserved=%d", classes.Long)
}
