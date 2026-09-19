package supervisor

import (
	"context"
	"time"
)

// ActionKey is intentionally closed: Supervisor cannot execute arbitrary work.
type ActionKey uint8

const (
	ActionRefreshRoute ActionKey = iota
	ActionRefreshAuth
	ActionCloseIdleTransports
	ActionClearNetworkDNS
)

const ownerActionTimeout = 5 * time.Second

// Owners exposes only narrow owner-scoped recovery operations. Authoritative
// subsystem state remains in those owners.
type Owners struct {
	RefreshRoute        func(context.Context) error
	RefreshAuth         func(context.Context) error
	CloseIdleTransports func(context.Context) error
	ClearNetworkDNS     func(context.Context) error
}

func (s *Supervisor) ownerAction(key ActionKey) func(context.Context) error {
	switch key {
	case ActionRefreshRoute:
		return s.owners.RefreshRoute
	case ActionRefreshAuth:
		return s.owners.RefreshAuth
	case ActionCloseIdleTransports:
		return s.owners.CloseIdleTransports
	case ActionClearNetworkDNS:
		return s.owners.ClearNetworkDNS
	default:
		return nil
	}
}

// Trigger schedules one named owner action if that action is not already in
// flight. It never executes owner I/O on the caller goroutine.
func (s *Supervisor) Trigger(key ActionKey) bool {
	action := s.ownerAction(key)
	if action == nil {
		return false
	}

	s.mu.Lock()
	if s.closed || s.inflight[key] {
		s.mu.Unlock()
		return false
	}
	s.inflight[key] = true
	s.wg.Add(1)
	s.mu.Unlock()

	go s.runAction(key, action)
	return true
}

func (s *Supervisor) runAction(key ActionKey, action func(context.Context) error) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, ownerActionTimeout)
	_ = action(ctx)
	cancel()

	s.mu.Lock()
	delete(s.inflight, key)
	s.mu.Unlock()
}

func (s *Supervisor) triggerNetworkRecovery() {
	s.Trigger(ActionRefreshRoute)
	s.Trigger(ActionRefreshAuth)
	s.Trigger(ActionCloseIdleTransports)
	s.Trigger(ActionClearNetworkDNS)
}
