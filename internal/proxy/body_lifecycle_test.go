package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/pavelsimo/pxgo/internal/config"
)

type countingReadCloser struct {
	io.Reader
	reads  int
	closed bool
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}

func (r *countingReadCloser) Close() error {
	r.closed = true
	return nil
}

type blockingReplaySource struct {
	once      sync.Once
	firstRead bool
	closed    chan struct{}
}

func newBlockingReplaySource() *blockingReplaySource {
	return &blockingReplaySource{closed: make(chan struct{})}
}

func (r *blockingReplaySource) Read(p []byte) (int, error) {
	if !r.firstRead {
		r.firstRead = true
		return copy(p, []byte("abcdefgh")), nil
	}
	<-r.closed
	return 0, errors.New("source closed")
}

func (r *blockingReplaySource) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func testReplayLimits(dir string) replayBodyLimits {
	return replayBodyLimits{
		memoryBytes:  4,
		maxBodyBytes: 32,
		tempDir:      dir,
	}
}

func TestAPISS0016KnownOversizeRejectedWithoutRead(t *testing.T) {
	src := &countingReadCloser{Reader: bytes.NewReader([]byte("12345"))}
	limits := testReplayLimits(t.TempDir())
	limits.maxBodyBytes = 4

	body, err := newReplayableBodyWithLimits(context.Background(), src, 5, limits, newReplayBudget(64))
	if !errors.Is(err, errReplayBodyTooLarge) {
		t.Fatalf("err=%v, want errReplayBodyTooLarge", err)
	}
	if body != nil {
		t.Fatalf("body=%v, want nil", body)
	}
	if src.reads != 0 {
		t.Fatalf("source was read %d times despite known oversize", src.reads)
	}
	if !src.closed {
		t.Fatal("source ownership was not closed on rejection")
	}
}

func TestAPISS0016UnknownOversizeCleansPartialSpool(t *testing.T) {
	dir := t.TempDir()
	limits := testReplayLimits(dir)
	limits.maxBodyBytes = 8
	budget := newReplayBudget(64)

	body, err := newReplayableBodyWithLimits(context.Background(), io.NopCloser(bytes.NewReader([]byte("123456789"))), -1, limits, budget)
	if !errors.Is(err, errReplayBodyTooLarge) {
		t.Fatalf("err=%v, want errReplayBodyTooLarge", err)
	}
	if body != nil {
		t.Fatalf("body=%v, want nil", body)
	}
	if used := budget.used.Load(); used != 0 {
		t.Fatalf("budget used=%d after rejected body", used)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("partial replay files leaked: %v", entries)
	}
}

func TestAPISS0016GlobalSpoolBudget(t *testing.T) {
	dir := t.TempDir()
	limits := testReplayLimits(dir)
	budget := newReplayBudget(8)

	first, err := newReplayableBodyWithLimits(context.Background(), io.NopCloser(bytes.NewReader([]byte("12345678"))), 8, limits, budget)
	if err != nil {
		t.Fatal(err)
	}
	if used := budget.used.Load(); used != 8 {
		t.Fatalf("budget used=%d want 8", used)
	}

	second, err := newReplayableBodyWithLimits(context.Background(), io.NopCloser(bytes.NewReader([]byte("abcde"))), 5, limits, budget)
	if !errors.Is(err, errReplaySpoolQuota) {
		t.Fatalf("err=%v, want errReplaySpoolQuota", err)
	}
	if second != nil {
		t.Fatalf("second body=%v, want nil", second)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if used := budget.used.Load(); used != 0 {
		t.Fatalf("budget used=%d after Close", used)
	}
}

func TestAPISS0016CancellationClosesSourceAndCleansSpool(t *testing.T) {
	dir := t.TempDir()
	limits := testReplayLimits(dir)
	budget := newReplayBudget(64)
	src := newBlockingReplaySource()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := newReplayableBodyWithLimits(ctx, src, -1, limits, budget)
		done <- err
	}()

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if used := budget.used.Load(); used != 0 {
		t.Fatalf("budget used=%d after cancellation", used)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial replay files leaked after cancellation: %v", entries)
	}
}

func TestAPISS0016CloseInvalidatesMemoryAndFileBodies(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "memory", payload: "1234"},
		{name: "file", payload: "12345678"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			budget := newReplayBudget(64)
			body, err := newReplayableBodyWithLimits(context.Background(), io.NopCloser(bytes.NewReader([]byte(tc.payload))), int64(len(tc.payload)), testReplayLimits(t.TempDir()), budget)
			if err != nil {
				t.Fatal(err)
			}
			if err := body.Close(); err != nil {
				t.Fatal(err)
			}
			if body.Size() != 0 {
				t.Fatalf("size=%d after Close", body.Size())
			}
			if body.path != "" || body.data != nil {
				t.Fatalf("body retained state after Close: path=%q data=%d", body.path, len(body.data))
			}
			if _, err := body.Open(); !errors.Is(err, errReplayBodyClosed) {
				t.Fatalf("Open err=%v, want errReplayBodyClosed", err)
			}
			if err := body.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}
			if used := budget.used.Load(); used != 0 {
				t.Fatalf("budget used=%d after idempotent Close", used)
			}
		})
	}
}

func TestAPISS0016RemoveFailureRemainsRetryable(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("directory permission semantics differ on Windows")
	}
	dir := t.TempDir()
	budget := newReplayBudget(64)
	body, err := newReplayableBodyWithLimits(context.Background(), io.NopCloser(bytes.NewReader([]byte("12345678"))), 8, testReplayLimits(dir), budget)
	if err != nil {
		t.Fatal(err)
	}
	path := body.path
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := body.Close(); err == nil {
		t.Fatal("expected remove failure")
	}
	if body.path != path || body.Size() == 0 {
		t.Fatalf("failed Close lost ownership: path=%q size=%d", body.path, body.Size())
	}
	if used := budget.used.Load(); used == 0 {
		t.Fatal("failed Close released budget while temp data still exists")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file remains after retry Close: %v", err)
	}
	if used := budget.used.Load(); used != 0 {
		t.Fatalf("budget used=%d after retry Close", used)
	}
}

func TestAPISS0016TempFilesStayInsideConfiguredDir(t *testing.T) {
	dir := t.TempDir()
	body, err := newReplayableBodyWithLimits(context.Background(), io.NopCloser(bytes.NewReader([]byte("12345678"))), 8, testReplayLimits(dir), newReplayBudget(64))
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if filepath.Dir(body.path) != dir {
		t.Fatalf("temp path=%q escaped configured dir %q", body.path, dir)
	}
}

func TestAPISS0016ServeHTTPKnownOversizeReturns413(t *testing.T) {
	cfg := config.Default()
	cfg.Server = "127.0.0.1:9"
	cfg.Username = "user"
	cfg.Password = "pass"

	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	src := &countingReadCloser{Reader: bytes.NewReader([]byte("tiny"))}
	req := httptest.NewRequest(http.MethodPost, "http://origin.example.test/upload", http.NoBody)
	req.Body = src
	req.ContentLength = defaultMaxReplayBodyBytes + 1

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want=%d body=%q", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if src.reads != 0 {
		t.Fatalf("source was read %d times despite known oversize", src.reads)
	}
}

func TestAPISS0016AuthRetryReplaysLargeBodyExactly(t *testing.T) {
	payload := bytes.Repeat([]byte("payload-"), (maxMemoryBody/8)+1024)
	received := make(chan []byte, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- data
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	parentCfg := config.Default()
	parentCfg.ClientAuth = "BASIC"
	parentCfg.ClientUsername = "user"
	parentCfg.ClientPassword = "pass"
	parent := startTestProxy(t, parentCfg)

	childCfg := config.Default()
	childCfg.Server = parent.ListenAddr()
	childCfg.Username = "user"
	childCfg.Password = "pass"
	child := startTestProxy(t, childCfg)

	req, err := http.NewRequest(http.MethodPost, origin.URL, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := proxyClient(t, child.Port()).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%s", resp.Status)
	}

	got := <-received
	if !bytes.Equal(got, payload) {
		t.Fatalf("replayed payload mismatch: got=%d want=%d", len(got), len(payload))
	}
}
