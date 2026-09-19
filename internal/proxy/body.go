package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
)

const (
	defaultMaxReplayBodyBytes  int64 = 256 << 20
	defaultMaxReplaySpoolBytes int64 = 512 << 20
)

var (
	errReplayBodyTooLarge = errors.New("replay body exceeds per-request limit")
	errReplaySpoolQuota   = errors.New("replay spool quota exceeded")
	errReplayBodyClosed   = errors.New("replay body is closed")

	defaultReplayBudget = newReplayBudget(defaultMaxReplaySpoolBytes)
)

type replayBodyLimits struct {
	memoryBytes  int64
	maxBodyBytes int64
	tempDir      string
}

func defaultReplayBodyLimits() replayBodyLimits {
	return replayBodyLimits{
		memoryBytes:  maxMemoryBody,
		maxBodyBytes: defaultMaxReplayBodyBytes,
	}
}

type replayBudget struct {
	max  int64
	used atomic.Int64
}

func newReplayBudget(max int64) *replayBudget {
	if max < 0 {
		max = 0
	}
	return &replayBudget{max: max}
}

func (b *replayBudget) reserve(n int64) bool {
	if n <= 0 {
		return true
	}
	for {
		current := b.used.Load()
		if current > b.max || n > b.max-current {
			return false
		}
		if b.used.CompareAndSwap(current, current+n) {
			return true
		}
	}
}

func (b *replayBudget) release(n int64) {
	if b == nil || n <= 0 {
		return
	}
	b.used.Add(-n)
}

type replayableBody struct {
	mu sync.Mutex

	data []byte
	path string
	size int64

	closed   bool
	reserved int64
	budget   *replayBudget
}

type ownedReadCloser struct {
	io.ReadCloser

	once sync.Once
	err  error
}

func (r *ownedReadCloser) Close() error {
	if r == nil || r.ReadCloser == nil {
		return nil
	}
	r.once.Do(func() {
		r.err = r.ReadCloser.Close()
	})
	return r.err
}

func newReplayableBody(src io.ReadCloser) (*replayableBody, error) {
	return newReplayableBodyWithLimits(context.Background(), src, -1, defaultReplayBodyLimits(), defaultReplayBudget)
}

func newReplayableBodyForRequest(ctx context.Context, src io.ReadCloser, contentLength int64) (*replayableBody, error) {
	return newReplayableBodyWithLimits(ctx, src, contentLength, defaultReplayBodyLimits(), defaultReplayBudget)
}

func newReplayableBodyWithLimits(ctx context.Context, src io.ReadCloser, contentLength int64, limits replayBodyLimits, budget *replayBudget) (*replayableBody, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if budget == nil {
		return nil, errors.New("nil replay budget")
	}
	if limits.memoryBytes < 0 || limits.maxBodyBytes < 0 || limits.memoryBytes > limits.maxBodyBytes {
		return nil, errors.New("invalid replay body limits")
	}
	if src == nil || src == http.NoBody {
		return &replayableBody{budget: budget}, nil
	}

	owned := &ownedReadCloser{ReadCloser: src}
	defer func() { _ = owned.Close() }()

	done := make(chan struct{})
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = owned.Close()
			case <-done:
			}
		}()
		defer close(done)
	}

	if contentLength >= 0 && contentLength > limits.maxBodyBytes {
		return nil, fmt.Errorf("%w: content length %d exceeds %d bytes", errReplayBodyTooLarge, contentLength, limits.maxBodyBytes)
	}

	var (
		memory   bytes.Buffer
		file     *os.File
		path     string
		reserved int64
		total    int64
	)

	cleanup := func(primary error) error {
		clear(memory.Bytes())

		var cleanupErr error
		if file != nil {
			if err := file.Close(); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("close replay temp file: %w", err))
			}
			file = nil
		}
		if path != "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove replay temp file %s: %w", path, err))
			} else {
				budget.release(reserved)
				reserved = 0
			}
		} else if reserved != 0 {
			budget.release(reserved)
			reserved = 0
		}
		return errors.Join(primary, cleanupErr)
	}

	writeChunk := func(chunk []byte) error {
		if file == nil && total+int64(len(chunk)) <= limits.memoryBytes {
			_, err := memory.Write(chunk)
			return err
		}

		if file == nil {
			var err error
			file, err = os.CreateTemp(limits.tempDir, "pxgo-body-*")
			if err != nil {
				return fmt.Errorf("create replay temp file: %w", err)
			}
			path = file.Name()

			needed := int64(memory.Len()) + int64(len(chunk))
			if !budget.reserve(needed) {
				return fmt.Errorf("%w: need %d bytes", errReplaySpoolQuota, needed)
			}
			reserved += needed

			if err := writeAll(file, memory.Bytes()); err != nil {
				return fmt.Errorf("write replay temp file: %w", err)
			}
			clear(memory.Bytes())
			memory.Reset()
			if err := writeAll(file, chunk); err != nil {
				return fmt.Errorf("write replay temp file: %w", err)
			}
			return nil
		}

		needed := int64(len(chunk))
		if !budget.reserve(needed) {
			return fmt.Errorf("%w: need %d additional bytes", errReplaySpoolQuota, needed)
		}
		reserved += needed
		if err := writeAll(file, chunk); err != nil {
			return fmt.Errorf("write replay temp file: %w", err)
		}
		return nil
	}

	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, cleanup(err)
		}

		n, readErr := owned.Read(buf)
		if n > 0 {
			if total+int64(n) > limits.maxBodyBytes {
				return nil, cleanup(fmt.Errorf("%w: body exceeds %d bytes", errReplayBodyTooLarge, limits.maxBodyBytes))
			}
			if err := writeChunk(buf[:n]); err != nil {
				return nil, cleanup(err)
			}
			total += int64(n)
		}

		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return nil, cleanup(err)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, cleanup(fmt.Errorf("read replay body: %w", readErr))
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, cleanup(err)
	}

	if file != nil {
		closeErr := file.Close()
		file = nil
		if closeErr != nil {
			removeErr := os.Remove(path)
			if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				budget.release(reserved)
				reserved = 0
				removeErr = nil
			}
			return nil, errors.Join(
				fmt.Errorf("close replay temp file: %w", closeErr),
				func() error {
					if removeErr != nil {
						return fmt.Errorf("remove replay temp file %s after close failure: %w", path, removeErr)
					}
					return nil
				}(),
			)
		}
		return &replayableBody{
			path:     path,
			size:     total,
			reserved: reserved,
			budget:   budget,
		}, nil
	}

	return &replayableBody{
		data:   memory.Bytes(),
		size:   total,
		budget: budget,
	}, nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := w.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (b *replayableBody) Open() (io.ReadCloser, error) {
	if b == nil {
		return http.NoBody, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, errReplayBodyClosed
	}
	if b.size == 0 {
		return http.NoBody, nil
	}
	if b.path != "" {
		// #nosec G703 -- b.path is created exclusively by os.CreateTemp inside this replay owner.
		return os.Open(b.path)
	}

	data := append([]byte(nil), b.data...)
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *replayableBody) Size() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

func (b *replayableBody) Close() error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}

	if b.path != "" {
		// #nosec G703 -- b.path is created exclusively by os.CreateTemp inside this replay owner.
		if err := os.Remove(b.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			b.mu.Unlock()
			return fmt.Errorf("remove replay temp file %s: %w", b.path, err)
		}
	}

	clear(b.data)
	b.data = nil
	b.path = ""
	b.size = 0
	b.closed = true

	reserved := b.reserved
	b.reserved = 0
	budget := b.budget
	b.mu.Unlock()

	if budget != nil {
		budget.release(reserved)
	}
	return nil
}
