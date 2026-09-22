package proxy

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestAPISS0022StartBlocksUntilShutdown(t *testing.T) {
	cfg := config.Default()
	cfg.Listen = "127.0.0.1"
	cfg.Port = 0
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Start() }()

	deadline := time.Now().Add(2 * time.Second)
	for s.Port() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Port() == 0 {
		t.Fatal("server did not become ready")
	}

	select {
	case err := <-done:
		t.Fatalf("Start returned while server was running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error after clean shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after shutdown")
	}
}

func TestAPISS0022MultiListenerBindFailureReturnsPromptly(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Listen = "127.0.0.1,::bad"
	cfg.Port = port
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = s.Start()
	if err == nil {
		t.Fatal("expected invalid second listener to fail")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("multi-listener bind failure took too long: %v", elapsed)
	}
	if s.Port() != cfg.Port {
		t.Fatalf("server port changed after bind failure: got=%d want=%d", s.Port(), cfg.Port)
	}

	rebound, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("first listener leaked after second bind failed: %v", err)
	}
	_ = rebound.Close()
}
