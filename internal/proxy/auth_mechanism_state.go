package proxy

import (
	"strings"
	"sync/atomic"
)

type authMechanismObservation struct {
	mechanism string
}

func (o *authMechanismObservation) ObserveHeader(header string) {
	if o == nil {
		return
	}
	next := classifyUpstreamAuthMechanism(header)
	if authMechanismSpecificity(next) == 0 {
		return
	}
	currentSpecificity := authMechanismSpecificity(o.mechanism)
	nextSpecificity := authMechanismSpecificity(next)
	if nextSpecificity < currentSpecificity {
		return
	}
	o.mechanism = next
}

func (o *authMechanismObservation) Result() string {
	if o == nil {
		return ""
	}
	return o.mechanism
}

func authMechanismSpecificity(mechanism string) int {
	switch {
	case strings.EqualFold(mechanism, authSchemeNeg):
		return 1
	case strings.EqualFold(mechanism, authSchemeBasic),
		strings.EqualFold(mechanism, authSchemeDigest),
		strings.EqualFold(mechanism, authNTLM),
		strings.EqualFold(mechanism, authMechanismKerberos):
		return 2
	default:
		return 0
	}
}

type authMechanismTracker struct {
	value atomic.Value
}

func (t *authMechanismTracker) Record(mechanism string) {
	if t == nil || authMechanismSpecificity(mechanism) == 0 {
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
