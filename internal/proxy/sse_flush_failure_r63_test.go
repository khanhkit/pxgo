package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

var errR63FailedFlush = errors.New("injected SSE downstream flush failure")

type r63FlushFailureWriter struct {
	header   http.Header
	deadline time.Time
	writes   int
	flushes  int
	failAt   int
}

func (w *r63FlushFailureWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *r63FlushFailureWriter) WriteHeader(int) {}
func (w *r63FlushFailureWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.deadline.IsZero() {
		return 0, errors.New("missing bounded SSE deadline")
	}
	return len(p), nil
}

func (w *r63FlushFailureWriter) SetWriteDeadline(t time.Time) error {
	w.deadline = t
	return nil
}
func (w *r63FlushFailureWriter) Flush() { w.flushes++ }
func (w *r63FlushFailureWriter) FlushError() error {
	w.flushes++
	if w.flushes == w.failAt {
		return errR63FailedFlush
	}
	return nil
}

type r63ChunkReader struct{ reader *bytes.Reader }

func (r *r63ChunkReader) Read(p []byte) (int, error) {
	if len(p) > 8192 {
		p = p[:8192]
	}
	return r.reader.Read(p)
}

func TestR63SSEStopsCopyAfterDownstreamFlushFailure(t *testing.T) {
	s := &Server{cfg: config.Default()}
	w := &r63FlushFailureWriter{failAt: 2}
	b := bytes.Repeat([]byte("data: long long long long\n\n"), 10000)
	s.writeHTTPResponse(w, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(&r63ChunkReader{reader: bytes.NewReader(b)})})
	if w.writes != 1 {
		t.Fatalf("R63_FLUSH_ERROR_NO_PROPAGATION writes=%d flushes=%d; expected only one body chunk before synthetic failed flush", w.writes, w.flushes)
	}
}

// The header flush itself must fail closed without reading or copying a body.
func TestR63SSEHeaderFlushFailureStopsBeforeBody(t *testing.T) {
	s := &Server{cfg: config.Default()}
	w := &r63FlushFailureWriter{failAt: 1}
	b := bytes.Repeat([]byte("data: still-not-sent\\n\\n"), 10000)
	s.writeHTTPResponse(w, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(&r63ChunkReader{reader: bytes.NewReader(b)}),
	})
	if w.writes != 0 {
		t.Fatalf("R63_HEADER_FLUSH_FAILURE_SILENCED writes=%d flushes=%d", w.writes, w.flushes)
	}
}
