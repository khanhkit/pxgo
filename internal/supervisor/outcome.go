package supervisor

import "time"

// ProxyKey is an opaque normalized identity supplied by the routing owner.
// Supervisor never parses it or derives route membership from it.
type ProxyKey string

// OutcomeKind classifies a runtime result at the narrowest layer that knows its
// origin. Only local explicit-proxy path failures may affect passive health.
type OutcomeKind uint8

const (
	OutcomeSuccess OutcomeKind = iota
	OutcomeClientCancelled
	OutcomeDestinationFailure
	OutcomeProxyDNSFailure
	OutcomeProxyDialFailure
	OutcomeProxyTLSFailure
	OutcomeProxyProtocolFailure
	OutcomeAuthExhausted
	OutcomeRouteFailure
	OutcomeInternalFailure
)

// PenalizesCandidate reports whether this outcome is attributable to the local
// path to an explicit upstream candidate.
func (k OutcomeKind) PenalizesCandidate() bool {
	switch k {
	case OutcomeProxyDNSFailure, OutcomeProxyDialFailure, OutcomeProxyTLSFailure, OutcomeProxyProtocolFailure:
		return true
	default:
		return false
	}
}

// Outcome deliberately contains no raw URL, headers, credentials, tokens or
// arbitrary error strings. Detailed owner diagnostics remain with their owner.
type Outcome struct {
	Kind     OutcomeKind
	Proxy    ProxyKey
	Duration time.Duration
}

// Candidate is an already-authoritative route member. Direct marks a DIRECT
// member supplied by routing/PAC policy; Supervisor never creates one.
type Candidate struct {
	Key    ProxyKey
	Direct bool
}
