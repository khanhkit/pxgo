package proxy

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type r149SlowCloseConn struct {
	net.Conn
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (c *r149SlowCloseConn) Close() error {
	c.once.Do(func() { close(c.started) })
	<-c.unblock
	return c.Conn.Close()
}

// Negative control on current PR107-descendant: one upstream Close may block
// synchronously in beginTunnelShutdown. That must not prevent other client
// socket close callbacks or the shutdown deadline from being respected.
func TestR149ShutdownDoesNotBlockOtherClientClosesBehindOneUpstream(t *testing.T) {
	s := &Server{tunnels: make(map[*managedTunnel]struct{}), tunnelZero: make(chan struct{})}
	const n = 2
	blocks := make([]*r149SlowCloseConn, n)
	started := make(chan struct{}, n)
	downstreamClosed := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		c, cOther := net.Pipe()
		t.Cleanup(func() { _ = cOther.Close() })
		upstream, uOther := net.Pipe()
		t.Cleanup(func() { _ = uOther.Close() })
		block := &r149SlowCloseConn{Conn: upstream, started: make(chan struct{}), unblock: make(chan struct{})}
		blocks[i] = block
		go func(b *r149SlowCloseConn) { <-b.started; started <- struct{}{} }(block)
		t.Cleanup(func() {
			select {
			case <-block.unblock:
			default:
				close(block.unblock)
			}
		})
		owner := &admissionConn{Conn: c, release: func() { downstreamClosed <- struct{}{} }}
		s.tunnels[&managedTunnel{client: owner, upstream: block}] = struct{}{}
	}
	beginReturned := make(chan struct{})
	go func() { s.beginTunnelShutdown(); close(beginReturned) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream Close not attempted")
	}
	// BeginShutdown should initiate close of all physical downstreams promptly,
	// even if an upstream TLS Close stalls. The original sequential loop does not.
	got := 0
	deadline := time.After(80 * time.Millisecond)
	for got < n {
		select {
		case <-downstreamClosed:
			got++
		case <-deadline:
			for _, b := range blocks {
				close(b.unblock)
			}
			t.Fatalf("R149_SHUTDOWN_BLOCKER: only %d/%d downstream sockets closed before unrelated upstream hang", got, n)
		}
	}
	for _, b := range blocks {
		close(b.unblock)
	}
	select {
	case <-beginReturned:
	case <-time.After(time.Second):
		t.Fatal("shutdown enumeration blocked")
	}
}

// A hung upstream.Close cannot defer the caller's context cancellation.
func TestR149ShutdownBeginReturnsAndDrainContextExpires(t *testing.T) {
	c, other := net.Pipe()
	defer other.Close()
	up, uOther := net.Pipe()
	defer uOther.Close()
	block := &r149SlowCloseConn{Conn: up, started: make(chan struct{}), unblock: make(chan struct{})}
	defer close(block.unblock)
	s := &Server{tunnels: make(map[*managedTunnel]struct{}), tunnelZero: make(chan struct{})}
	s.tunnels[&managedTunnel{client: c, upstream: block}] = struct{}{}
	beginReturned := make(chan (<-chan struct{}), 1)
	go func() { beginReturned <- s.beginTunnelShutdown() }()
	select {
	case done := <-beginReturned:
		select {
		case <-block.started:
		case <-time.After(time.Second):
			t.Fatal("upstream Close not called")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if e := waitTunnelDrain(ctx, done); !errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("waitTunnelDrain needs context deadline, got %v", e)
		}
	case <-time.After(60 * time.Millisecond):
		t.Fatal("beginTunnelShutdown synchronously blocked behind upstream Close")
	}
}
