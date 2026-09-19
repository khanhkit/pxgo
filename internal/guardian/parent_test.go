package guardian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	helperModeEnv  = "PXGO_GUARDIAN_TEST_MODE"
	helperCountEnv = "PXGO_GUARDIAN_TEST_COUNT"
	helperAliveEnv = "PXGO_GUARDIAN_TEST_ALIVE"
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
	if mode == "parent-host" {
		alivePath := os.Getenv(helperAliveEnv)
		env := replaceTestEnv(os.Environ(), helperModeEnv, "bridge-worker")
		env = replaceTestEnv(env, helperAliveEnv, alivePath)
		spec := CommandSpec{
			Path:   os.Args[0],
			Args:   []string{"-test.run=TestGuardianHelperProcess"},
			Env:    env,
			Stdout: io.Discard,
			Stderr: io.Discard,
		}
		if err := RunParent(context.Background(), spec, ParentOptions{HangWindow: time.Second, WatchInterval: 50 * time.Millisecond, StopTimeout: 100 * time.Millisecond}); err != nil {
			os.Exit(93)
		}
		return
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
	case "ready-normal-exit":
		_ = session.Send(ctx, Message{Type: MessageReady})
		_ = session.Send(ctx, Message{Type: MessageBeat, Sequence: 1})
		_ = session.Send(ctx, Message{Type: MessageStop})
		time.Sleep(20 * time.Millisecond)
		os.Exit(0)
	case "ready-crash":
		_ = session.Send(ctx, Message{Type: MessageReady})
		_ = session.Send(ctx, Message{Type: MessageBeat, Sequence: 1})
		time.Sleep(20 * time.Millisecond)
		os.Exit(42)
	case "ready-hang":
		_ = session.Send(ctx, Message{Type: MessageReady})
		_ = session.Send(ctx, Message{Type: MessageBeat, Sequence: 1})
		select {}
	case "ready-ignore-stop":
		_ = session.Send(ctx, Message{Type: MessageReady})
		_ = session.Send(ctx, Message{Type: MessageBeat, Sequence: 1})
		select {}
	case "ready-close-control":
		_ = session.Send(ctx, Message{Type: MessageReady})
		_ = session.Send(ctx, Message{Type: MessageBeat, Sequence: 1})
		_ = session.Close()
		select {}
	case "bridge-worker":
		alivePath := os.Getenv(helperAliveEnv)
		if alivePath == "" {
			os.Exit(94)
		}
		if err := os.WriteFile(alivePath, []byte("alive"), 0o600); err != nil {
			os.Exit(95)
		}
		defer os.Remove(alivePath)
		server := newFakeWorkerServer()
		server.ready.Store(true)
		server.progress.Store(1)
		result := RunWorker(context.Background(), session, workerHooks(server), testWorkerOptions())
		if result.Exit != WorkerExitNormal {
			t.Fatalf("bridge worker result=%+v", result)
		}
		return
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

func replaceTestEnv(base []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+value)
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

func TestTCGUARDPARENT030StopIgnoredIsForceKilledAndReaped(t *testing.T) {
	spec, countPath := helperSpec(t, "ready-ignore-stop")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunParent(ctx, spec, fastParentOptions()) }()
	waitForGenerations(t, countPath, 1)
	time.Sleep(30 * time.Millisecond)
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunParent forced stop=%v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("forced stop took %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("STOP-ignoring worker was not force-killed/reaped")
	}
	if got := generationCount(countPath); got != 1 {
		t.Fatalf("forced normal stop generations=%d, want 1", got)
	}
}

func TestTCGUARDPARENT029ControlCloseRecyclesImmediately(t *testing.T) {
	spec, countPath := helperSpec(t, "ready-close-control")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunParent(ctx, spec, fastParentOptions()) }()
	waitForGenerations(t, countPath, 2)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunParent after control close=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("control-close recycle did not stop after cancellation")
	}
}

func TestTCGUARDPARENT031ParentDeathLeavesNoWorkerOrphan(t *testing.T) {
	alivePath := filepath.Join(t.TempDir(), "worker.alive")
	env := replaceTestEnv(os.Environ(), helperModeEnv, "parent-host")
	env = replaceTestEnv(env, helperAliveEnv, alivePath)
	cmd := exec.Command(os.Args[0], "-test.run=TestGuardianHelperProcess")
	cmd.Env = env
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFileState(t, alivePath, true, 3*time.Second)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waitForFileState(t, alivePath, false, 3*time.Second)
}

func waitForFileState(t *testing.T, path string, wantExists bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := os.Stat(path)
		exists := err == nil
		if exists == wantExists {
			return
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s existence=%v, want %v", path, fileExists(path), wantExists)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestTCGUARDPARENT030ReadyNormalExitTerminatesParent(t *testing.T) {
	spec, countPath := helperSpec(t, "ready-normal-exit")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := RunParent(ctx, spec, fastParentOptions()); err != nil {
		t.Fatalf("ready normal exit=%v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("ready normal exit did not terminate parent before deadline: %v", ctx.Err())
	}
	if got := generationCount(countPath); got != 1 {
		t.Fatalf("ready normal exit generations=%d, want 1", got)
	}
}
