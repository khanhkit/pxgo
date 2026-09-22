package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/proxy"
)

type blockingShutdowner struct{}

func (blockingShutdowner) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

type errorShutdowner struct {
	err error
}

func (s errorShutdowner) Shutdown(context.Context) error {
	return s.err
}

func TestAPISS0022ConnectionRefusedUsesTypedError(t *testing.T) {
	err := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
	}
	if !isConnectionRefused(err) {
		t.Fatalf("typed ECONNREFUSED was not recognized: %v", err)
	}
	if isConnectionRefused(errors.New("localized text: connection refused")) {
		t.Fatal("string content alone classified connection refused")
	}
}

func TestAPISS0022ShutdownWithTimeoutIsBounded(t *testing.T) {
	start := time.Now()
	err := shutdownWithTimeout(blockingShutdowner{}, 25*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want context deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown exceeded bound: %v", elapsed)
	}
}

func TestAPISS0022ShutdownWithTimeoutReturnsServerError(t *testing.T) {
	want := errors.New("shutdown failed")
	if err := shutdownWithTimeout(errorShutdowner{err: want}, time.Second); !errors.Is(err, want) {
		t.Fatalf("err=%v want %v", err, want)
	}
}

func TestAPISS0022DoSelfTestRejectsNilRequest(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("doSelfTestRequest panicked for nil request: %v", recovered)
		}
	}()
	resp, err := doSelfTestRequest(&http.Client{}, nil, config.Default(), false)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected nil self-test request error")
	}
}

func TestAPISS0022SelfTestReadinessDoesNotCreateProbeConnection(t *testing.T) {
	cfg := config.Default()
	cfg.Listen = "127.0.0.1"
	cfg.Port = 0
	s, err := proxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Start() }()

	if err := waitSelfTestReady(s, errc, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := shutdownWithTimeout(s, 250*time.Millisecond); err != nil {
		t.Fatalf("shutdown after readiness wait: %v", err)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Start returned error after shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after shutdown")
	}
}

func TestAPISS0022SelfTestStartFailureDoesNotDoubleWait(t *testing.T) {
	cfg := config.Default()
	cfg.Listen = "127.0.0.1,::bad"
	cfg.Port = freePort(t)
	cfg.Test = "http://127.0.0.1"

	start := time.Now()
	err := runSelfTest(cfg)
	if err == nil {
		t.Fatal("expected self-test listener start failure")
	}
	if strings.Contains(err.Error(), "did not stop after shutdown") {
		t.Fatalf("start failure was polluted by second errc wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 750*time.Millisecond {
		t.Fatalf("start failure cleanup took too long: %v", elapsed)
	}
}

func TestAPISS0022MalformedSelfTestURLReturnsErrorWithoutPanic(t *testing.T) {
	bin := buildPx(t)
	port := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin,
		"--port="+strconv.Itoa(port),
		"--test=http://[::1",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("malformed self-test URL unexpectedly succeeded: %s", out)
	}
	if strings.Contains(string(out), "panic:") {
		t.Fatalf("malformed self-test URL panicked:\n%s", out)
	}
}
