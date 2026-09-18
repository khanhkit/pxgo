package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strconv"

	"github.com/pavelsimo/pxgo/internal/supervisor"
	"github.com/pavelsimo/pxgo/internal/wproxy"
)

func proxyKeyForSupervisor(server wproxy.Server) supervisor.ProxyKey {
	if server == wproxy.Direct {
		return supervisor.ProxyKey("direct://DIRECT:80")
	}
	return supervisor.ProxyKey(proxyScheme(server) + "://" + net.JoinHostPort(server.Host, strconv.Itoa(server.Port)))
}

func supervisorCandidate(server wproxy.Server) supervisor.Candidate {
	return supervisor.Candidate{
		Key:    proxyKeyForSupervisor(server),
		Direct: server == wproxy.Direct,
	}
}

func orderProxyCandidates(sup *supervisor.Supervisor, authoritative []wproxy.Server) []wproxy.Server {
	if sup == nil || len(authoritative) < 2 {
		return append([]wproxy.Server(nil), authoritative...)
	}

	candidates := make([]supervisor.Candidate, len(authoritative))
	byKey := make(map[supervisor.ProxyKey][]wproxy.Server, len(authoritative))
	for i, server := range authoritative {
		candidate := supervisorCandidate(server)
		candidates[i] = candidate
		byKey[candidate.Key] = append(byKey[candidate.Key], server)
	}

	ordered := sup.Order(candidates)
	result := make([]wproxy.Server, 0, len(ordered))
	for _, candidate := range ordered {
		servers := byKey[candidate.Key]
		if len(servers) == 0 {
			// Defensive fallback: ordering is not allowed to manufacture
			// membership, so preserve the authoritative list if it ever does.
			return append([]wproxy.Server(nil), authoritative...)
		}
		result = append(result, servers[0])
		byKey[candidate.Key] = servers[1:]
	}
	return result
}

func classifyProxyTransportOutcome(ctx context.Context, candidate wproxy.Server, err error) supervisor.OutcomeKind {
	if ctx != nil && ctx.Err() != nil {
		return supervisor.OutcomeClientCancelled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return supervisor.OutcomeClientCancelled
	}
	if candidate == wproxy.Direct {
		return supervisor.OutcomeDestinationFailure
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return supervisor.OutcomeProxyDNSFailure
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return supervisor.OutcomeProxyTLSFailure
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return supervisor.OutcomeProxyTLSFailure
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return supervisor.OutcomeProxyTLSFailure
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return supervisor.OutcomeProxyTLSFailure
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return supervisor.OutcomeProxyDialFailure
	}
	return supervisor.OutcomeProxyProtocolFailure
}
