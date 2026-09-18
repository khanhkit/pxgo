package proxy

import (
	"net"
	"sync"
)

type admissionListener struct {
	net.Listener
	slots     chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newAdmissionListener(listener net.Listener, slots chan struct{}) net.Listener {
	return &admissionListener{Listener: listener, slots: slots, done: make(chan struct{})}
}

func (l *admissionListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	select {
	case l.slots <- struct{}{}:
		return &admissionConn{Conn: conn, release: func() { <-l.slots }}, nil
	case <-l.done:
		_ = conn.Close()
		return nil, net.ErrClosed
	}
}

func (l *admissionListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type admissionConn struct {
	net.Conn
	release     func()
	releaseOnce sync.Once
}

func (c *admissionConn) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
}
