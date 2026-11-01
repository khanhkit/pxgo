package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

type r59GuardRW struct {
	h          http.Header
	b          bytes.Buffer
	deadlines  []time.Time
	deadline   time.Time
	badWrites  int
	badFlushes int
}

func (w *r59GuardRW) Header() http.Header {
	if w.h == nil {
		w.h = make(http.Header)
	}
	return w.h
}
func (w *r59GuardRW) WriteHeader(int) {}
func (w *r59GuardRW) Write(p []byte) (int, error) {
	if w.deadline.IsZero() {
		w.badWrites++
	}
	return w.b.Write(p)
}

func (w *r59GuardRW) Flush() {
	if w.deadline.IsZero() {
		w.badFlushes++
	}
}

func (w *r59GuardRW) SetWriteDeadline(d time.Time) error {
	w.deadline = d
	w.deadlines = append(w.deadlines, d)
	return nil
}

func TestR59SSEWriteDeadlineIsBoundedPerFlush(t *testing.T) {
	w := &r59GuardRW{}
	s := &Server{cfg: config.Default()}
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: ok\n\n"))}
	s.writeHTTPResponse(w, resp)
	if w.b.String() != "data: ok\n\n" || w.badWrites != 0 || w.badFlushes != 0 || !w.deadline.IsZero() {
		t.Fatalf("SSE not bounded or not restored: badWrites=%d badFlushes=%d body=%q deadline=%v", w.badWrites, w.badFlushes, w.b.String(), w.deadline)
	}
	if len(w.deadlines) != 4 || w.deadlines[0].IsZero() || !w.deadlines[1].IsZero() || w.deadlines[2].IsZero() || !w.deadlines[3].IsZero() {
		t.Fatalf("deadline sequence=%v", w.deadlines)
	}
}
