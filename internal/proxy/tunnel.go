package proxy

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
)

type managedTunnel struct {
	client    net.Conn
	upstream  net.Conn
	closeOnce sync.Once
}

func (t *managedTunnel) Close() {
	if t == nil {
		return
	}
	t.closeOnce.Do(func() {
		_ = t.client.Close()
		_ = t.upstream.Close()
	})
}

func closedSignal() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// reserveTunnel closes the race between http.Hijacker ownership transfer and
// Server.Shutdown. A reservation is created before Hijack; Shutdown prevents
// new reservations and waits for every existing reservation to either abort or
// become a registered tunnel and finish.
func (s *Server) reserveTunnel() bool {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	if s.tunnelShuttingDown {
		return false
	}
	if s.tunnelPending == 0 && len(s.tunnels) == 0 {
		s.tunnelZero = make(chan struct{})
	}
	s.tunnelPending++
	return true
}

func (s *Server) cancelTunnelReservation() {
	s.tunnelMu.Lock()
	if s.tunnelPending > 0 {
		s.tunnelPending--
	}
	s.signalTunnelZeroLocked()
	s.tunnelMu.Unlock()
}

func (s *Server) activateTunnel(client, upstream net.Conn) (*managedTunnel, bool) {
	s.tunnelMu.Lock()
	if s.tunnelPending > 0 {
		s.tunnelPending--
	}
	if s.tunnelShuttingDown {
		s.signalTunnelZeroLocked()
		s.tunnelMu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return nil, false
	}
	t := &managedTunnel{client: client, upstream: upstream}
	s.tunnels[t] = struct{}{}
	atomic.AddInt64(&s.active, 1)
	s.tunnelMu.Unlock()
	return t, true
}

func (s *Server) finishTunnel(t *managedTunnel) {
	if t == nil {
		return
	}
	s.tunnelMu.Lock()
	if _, ok := s.tunnels[t]; ok {
		delete(s.tunnels, t)
		atomic.AddInt64(&s.active, -1)
		s.signalTunnelZeroLocked()
	}
	s.tunnelMu.Unlock()
}

func (s *Server) closeAndFinishTunnel(t *managedTunnel) {
	t.Close()
	s.finishTunnel(t)
}

func (s *Server) signalTunnelZeroLocked() {
	if s.tunnelPending != 0 || len(s.tunnels) != 0 || s.tunnelZero == nil {
		return
	}
	select {
	case <-s.tunnelZero:
	default:
		close(s.tunnelZero)
	}
}

func (s *Server) beginTunnelShutdown() <-chan struct{} {
	s.tunnelMu.Lock()
	s.tunnelShuttingDown = true
	done := s.tunnelZero
	tunnels := make([]*managedTunnel, 0, len(s.tunnels))
	for tunnel := range s.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	s.tunnelMu.Unlock()
	for _, tunnel := range tunnels {
		tunnel.Close()
	}
	return done
}

func waitTunnelDrain(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
