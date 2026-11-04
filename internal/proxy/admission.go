package proxy

import (
	"net"
	"sync"
	"time"
)

type admissionListener struct {
	net.Listener
	slots     chan struct{}
	governor  *capacityGovernor
	wait      time.Duration
	done      chan struct{}
	closeOnce sync.Once
}

func newAdmissionListener(listener net.Listener, slots chan struct{}) net.Listener {
	return &admissionListener{Listener: listener, slots: slots, done: make(chan struct{})}
}

func (l *admissionListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		if l.governor != nil && l.governor.mode == modeAuto {
			l.governor.recordAcceptError(err)
		}
		return nil, err
	}
	if l.governor != nil && l.governor.mode == modeAuto {
		if err := l.governor.acquire(l.done, l.wait); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return &admissionConn{Conn: conn, release: l.governor.release}, nil
	}
	select {
	case l.slots <- struct{}{}:
		if l.governor != nil {
			l.governor.observeAdmission()
		}
		return &admissionConn{Conn: conn, release: func() {
			<-l.slots
			if l.governor != nil {
				l.governor.release()
			}
		}}, nil
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

// A dynamic listener accepts at most one socket while waiting for a shared
// capacity permit; net/http.Server handles temporary timeout backoff itself.
func newDynamicAdmissionListener(listener net.Listener, governor *capacityGovernor, wait time.Duration) net.Listener {
	return &admissionListener{Listener: listener, done: make(chan struct{}), governor: governor, wait: wait}
}
