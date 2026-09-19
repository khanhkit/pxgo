package guardian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	helperModeEnv  = "PXGO_GUARDIAN_TEST_MODE"
	helperCountEnv = "PXGO_GUARDIAN_TEST_COUNT"
)

func TestGuardianHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	countPath := os.Getenv(helperCountEnv)
	if countPath != "" {
		f, err := os.OpenFile(countPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(f, os.Getpid())
			_ = f.Close()
		}
	}

	if mode == "pre-ready-exit" {
		os.Exit(23)
	}

	control, ok, err := WorkerControlFromEnv()
	if err != nil || !ok {
		os.Exit(90)
	}
	ctx := context.Background()
	session, err := Connect(ctx, control.Addr, control.Token)
	if err != nil {
		os.Exit(91)
	}

	switch mode {
	case "ready-crash":
		_ = session.Send(ctx, Message{Type: MessageReady})
		_ = session.Send(ctx, Message{Type: MessageBeat, Sequence: 1})
		time.Sleep(20 * time.Millisecond)
		os.Exit(42)
	case "ready-hang":
		_ = session.Send(ctx, Message{Type: MessageReady})
		_ = session.Send(ctx, Message{Type: MessageBeat, Sequence: 1})
		select {}
	case "ready-stop":
		_ = session.Send(ctx, Message{Type: MessageReady})
		var seq uint64 = 1
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		msgc := make(chan Message, 1)
		go func() {
			for {
				msg, readErr := session.Read(ctx)
				if readErr != nil {
					return
				}
				msgc <- msg
			}
		}()
		for {
			select {
			case <-ticker.C:
				seq++
				_ = session.Send(ctx, Message{Type: MessageBeat, Sequence: seq})
			case msg := <-msgc:
				if msg.Type == MessageStop {
					_ = session.Close()
					os.Exit(0)
				}
			}
		}
	default:
		os.Exit(92)
	}
}

func helperSpec(t *testing.T, mode string) (CommandSpec, string) {
	t.Helper()
	countPath := filepath.Join(t.TempDir(), "generations.log")
	env := append([]string(nil), os.Environ()...)
	env = append(env, helperModeEnv+"="+mode, helperCountEnv+"="+countPath)
	return CommandSpec{
		Path:   os.Args[0],
		Args:   []string{"-test.run=TestGuardianHelperProcess"},
		Env:    env,
		Stdout: io.Discard,
		Stderr: io.Discard,
	}, countPath
}

func fastParentOptions() ParentOptions {
	return ParentOptions{
		HangWindow:      80 * time.Millisecond,
		WatchInterval:   10 * time.Millisecond,
		StopTimeout:     50 * time.Millisecond,
		RestartSchedule: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond},
		StableRunReset:  time.Second,
	}
}

func generationCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

func waitForGenerations(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if generationCount(path) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("generation count=%d, want >=%d", generationCount(path), want)
}

func TestTCGUARDPARENT027PreReadyExitDoesNotRestart(t *testing.T) {
	spec, countPath := helperSpec(t, "pre-ready-exit")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := RunParent(ctx, spec, fastParentOptions())
	var startup *StartupExitError
	if !errors.As(err, &startup) {
		t.Fatalf("RunParent error=%v, want StartupExitError", err)
	}
	if startup.Code != 23 {
		t.Fatalf("startup exit code=%d, want 23", startup.Code)
	}
	if got := generationCount(countPath); got != 1 {
		t.Fatalf("pre-ready generations=%d, want 1", got)
	}
}

func TestTCGUARDPARENT028ReadyCrashRestarts(t *testing.T) {
	spec, countPath := helperSpec(t, "ready-crash")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunParent(ctx, spec, fastParentOptions()) }()

	waitForGenerations(t, countPath, 2)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunParent after cancel=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parent did not stop after cancellation")
	}
}

func TestTCGUARDPARENT028HeartbeatHangRecycles(t *testing.T) {
	spec, countPath := helperSpec(t, "ready-hang")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunParent(ctx, spec, fastParentOptions()) }()

	waitForGenerations(t, countPath, 2)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunParent after hang/cancel=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hung parent did not stop")
	}
}

func TestTCGUARDPARENT030ContextStopIsNormalAndDoesNotRestart(t *testing.T) {
	spec, countPath := helperSpec(t, "ready-stop")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunParent(ctx, spec, fastParentOptions()) }()

	waitForGenerations(t, countPath, 1)
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunParent normal stop=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parent did not stop normally")
	}
	if got := generationCount(countPath); got != 1 {
		t.Fatalf("normal stop generations=%d, want 1", got)
	}
}
