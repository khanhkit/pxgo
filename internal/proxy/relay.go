package proxy

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// relay pumps bytes between the two ends of a CONNECT tunnel. When idle
// tracking is disabled, raw io.Copy keeps the platform zero-copy path. When
// idle tracking is enabled, copyDirection observes each successful read so an
// active one-way transfer refreshes tunnel-wide activity before a bulk copy
// call returns. Clean EOF still half-closes only the destination write side.
func relay(a, b net.Conn, idle time.Duration) {
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		if copyDirection(dst, src, idle, &lastActivity) {
			halfClose(dst)
			return
		}
		// Real error or idle expiry: tear the whole tunnel down.
		_ = a.Close()
		_ = b.Close()
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

type activityReader struct {
	conn         net.Conn
	wait         time.Duration
	lastActivity *atomic.Int64
}

func (r *activityReader) Read(p []byte) (int, error) {
	if r.wait > 0 {
		_ = r.conn.SetReadDeadline(time.Now().Add(r.wait))
	}
	n, err := r.conn.Read(p)
	if n > 0 {
		r.lastActivity.Store(time.Now().UnixNano())
	}
	return n, err
}

// copyDirection copies src to dst until EOF, a real error, or tunnel-wide idle
// expiry. It reports whether the copy ended in a clean EOF. With idle tracking
// enabled, every successful source read updates shared activity and refreshes
// that direction's read deadline. A short grace re-check protects against both
// directions reaching their deadline at the same scheduler instant.
func copyDirection(dst, src net.Conn, idle time.Duration, lastActivity *atomic.Int64) bool {
	if idle <= 0 {
		_, err := io.Copy(dst, src)
		return err == nil
	}
	grace := idle / 10
	if grace > 100*time.Millisecond {
		grace = 100 * time.Millisecond
	}
	if grace <= 0 {
		grace = time.Nanosecond
	}
	graced := false
	wait := idle
	for {
		reader := &activityReader{conn: src, wait: wait, lastActivity: lastActivity}
		_, err := io.Copy(dst, reader)
		if err == nil {
			return true // EOF
		}
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			return false
		}
		if time.Since(time.Unix(0, lastActivity.Load())) < idle {
			graced = false
			wait = idle
			continue
		}
		if !graced {
			graced = true
			wait = grace
			continue
		}
		return false // tunnel idle
	}
}

type closeWriter interface{ CloseWrite() error }

func halfClose(c net.Conn) {
	if cw, ok := c.(closeWriter); ok { // *net.TCPConn and *tls.Conn
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
