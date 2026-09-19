package proxy

import "sync/atomic"

type authMechanismTracker struct {
	value atomic.Value
}

func (t *authMechanismTracker) ObserveHeader(header string) {
	if t == nil {
		return
	}
	mechanism := classifyUpstreamAuthMechanism(header)
	if mechanism == "" {
		return
	}
	t.value.Store(mechanism)
}

func (t *authMechanismTracker) Snapshot() string {
	if t == nil {
		return ""
	}
	value := t.value.Load()
	if value == nil {
		return ""
	}
	mechanism, _ := value.(string)
	return mechanism
}
