package guardian

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeWorkerServer struct {
	ready    atomic.Bool
	progress atomic.Uint64
	fatal    atomic.Bool
	started  chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
	snaps    atomic.Int32
}

func newFakeWorkerServer() *fakeWorkerServer {
	return &fakeWorkerServer{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (f *fakeWorkerServer) Start() error {
	close(f.started)
	<-f.stopped
	return nil
}

func (f *fakeWorkerServer) Ready() bool { return f.ready.Load() }

func (f *fakeWorkerServer) Progress() uint64 { return f.progress.Load() }

func (f *fakeWorkerServer) FatalRequested() bool { return f.fatal.Load() }

func (f *fakeWorkerServer) Shutdown(context.Context) error {
	f.stopOnce.Do(func() { close(f.stopped) })
	return nil
}

func (f *fakeWorkerServer) Snapshot() { f.snaps.Add(1) }

func workerHooks(f *fakeWorkerServer) WorkerHooks {
	return WorkerHooks{
		Start:          f.Start,
		Ready:          f.Ready,
		Progress:       f.Progress,
		FatalRequested: f.FatalRequested,
		Shutdown:       f.Shutdown,
		Snapshot:       f.Snapshot,
	}
}

func testWorkerSessions(t *testing.T) (*Session, *Session) {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	return newSession(left), newSession(right)
}

func testWorkerOptions() WorkerOptions {
	return WorkerOptions{
		HeartbeatInterval: 10 * time.Millisecond,
		ReadyPollInterval: 2 * time.Millisecond,
		StopTimeout:       100 * time.Millisecond,
	}
}

func TestTCGUARDWORKER020ReadyAndProgressOrdering(t *testing.T) {
	worker, parent := testWorkerSessions(t)
	server := newFakeWorkerServer()
	result := make(chan WorkerResult, 1)
	go func() {
		result <- RunWorker(context.Background(), worker, workerHooks(server), testWorkerOptions())
	}()
	<-server.started

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := parent.Read(contextWithTimeout(t, 25*time.Millisecond)); err == nil {
		t.Fatal("worker sent protocol message before server readiness")
	}

	server.progress.Store(7)
	server.ready.Store(true)
	msg, err := parent.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != MessageReady {
		t.Fatalf("first worker message=%+v, want READY", msg)
	}
	msg, err = parent.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != MessageBeat || msg.Sequence != 7 {
		t.Fatalf("second worker message=%+v, want BEAT 7", msg)
	}

	if err := parent.Send(ctx, Message{Type: MessageStop}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.Exit != WorkerExitNormal || got.Err != nil {
			t.Fatalf("worker result=%+v", got)
		}
	case <-ctx.Done():
		t.Fatal("worker did not stop")
	}
}

func TestTCGUARDWORKER023ControlLossStopsWorker(t *testing.T) {
	worker, parent := testWorkerSessions(t)
	server := newFakeWorkerServer()
	server.ready.Store(true)
	result := make(chan WorkerResult, 1)
	go func() {
		result <- RunWorker(context.Background(), worker, workerHooks(server), testWorkerOptions())
	}()
	<-server.started

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, err := parent.Read(ctx)
	if err != nil || msg.Type != MessageReady {
		t.Fatalf("READY=%+v err=%v", msg, err)
	}
	_ = parent.Close()

	select {
	case got := <-result:
		if got.Exit != WorkerExitNormal {
			t.Fatalf("control-loss result=%+v", got)
		}
	case <-ctx.Done():
		t.Fatal("worker became orphan after control loss")
	}
}

func TestTCGUARDWORKER024FatalSnapshotsAndRequestsRestart(t *testing.T) {
	worker, parent := testWorkerSessions(t)
	server := newFakeWorkerServer()
	server.ready.Store(true)
	server.progress.Store(1)
	result := make(chan WorkerResult, 1)
	go func() {
		result <- RunWorker(context.Background(), worker, workerHooks(server), testWorkerOptions())
	}()
	<-server.started

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, err := parent.Read(ctx)
	if err != nil || msg.Type != MessageReady {
		t.Fatalf("READY=%+v err=%v", msg, err)
	}

	server.fatal.Store(true)
	select {
	case got := <-result:
		if got.Exit != WorkerExitRestart {
			t.Fatalf("fatal result=%+v", got)
		}
		if server.snaps.Load() != 1 {
			t.Fatalf("snapshot calls=%d, want 1", server.snaps.Load())
		}
	case <-ctx.Done():
		t.Fatal("fatal worker did not exit")
	}
}

func contextWithTimeout(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return ctx
}
