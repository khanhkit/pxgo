package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/pavelsimo/pxgo/internal/supervisor"
	"github.com/pavelsimo/pxgo/internal/wproxy"
)

func TestTCSUPROUTE005ProxyCandidatesNeverInventDirect(t *testing.T) {
	if got := proxyCandidates(nil); len(got) != 0 {
		t.Fatalf("empty authoritative route became %#v; Supervisor/proxy path must not invent DIRECT", got)
	}
}

func TestTCSUPROUTE005SupervisorOrderingPreservesServerMembership(t *testing.T) {
	sup := supervisor.New(supervisor.Owners{})
	defer sup.Close()

	a := wproxy.Server{Host: "proxy-a.example", Port: 8080, Scheme: "http"}
	b := wproxy.Server{Host: "proxy-b.example", Port: 8080, Scheme: "http"}
	authoritative := []wproxy.Server{a, wproxy.Direct, b}
	keyA := proxyKeyForSupervisor(a)
	sup.Record(supervisor.Outcome{Kind: supervisor.OutcomeProxyDialFailure, Proxy: keyA})
	sup.Record(supervisor.Outcome{Kind: supervisor.OutcomeProxyDialFailure, Proxy: keyA})

	got := orderProxyCandidates(sup, authoritative)
	want := []wproxy.Server{b, wproxy.Direct, a}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered proxies = %#v, want %#v", got, want)
	}
}

func TestTCSUPHTTP016TypedTransportClassification(t *testing.T) {
	proxyCandidate := wproxy.Server{Host: "proxy.example", Port: 8443, Scheme: "https"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name      string
		ctx       context.Context
		candidate wproxy.Server
		err       error
		want      supervisor.OutcomeKind
	}{
		{"client cancellation", cancelled, proxyCandidate, errors.New("transport stopped"), supervisor.OutcomeClientCancelled},
		{"direct destination dns", context.Background(), wproxy.Direct, &net.DNSError{Err: "no such host", Name: "origin.example"}, supervisor.OutcomeDestinationFailure},
		{"proxy dns", context.Background(), proxyCandidate, &net.DNSError{Err: "no such host", Name: "proxy.example"}, supervisor.OutcomeProxyDNSFailure},
		{"proxy dial", context.Background(), proxyCandidate, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, supervisor.OutcomeProxyDialFailure},
		{"proxy cert", context.Background(), proxyCandidate, x509.UnknownAuthorityError{}, supervisor.OutcomeProxyTLSFailure},
		{"proxy tls record", context.Background(), proxyCandidate, tls.RecordHeaderError{}, supervisor.OutcomeProxyTLSFailure},
		{"proxy protocol fallback", context.Background(), proxyCandidate, errors.New("malformed upstream response"), supervisor.OutcomeProxyProtocolFailure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProxyTransportOutcome(tc.ctx, tc.candidate, tc.err); got != tc.want {
				t.Fatalf("kind = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTCSUPHTTP016ProxyKeyIsStableAndSchemeAware(t *testing.T) {
	tests := []struct {
		server wproxy.Server
		want   supervisor.ProxyKey
	}{
		{wproxy.Direct, "direct://DIRECT:80"},
		{wproxy.Server{Host: "proxy.example", Port: 8080, Scheme: "http"}, "http://proxy.example:8080"},
		{wproxy.Server{Host: "proxy.example", Port: 8443, Scheme: "https"}, "https://proxy.example:8443"},
		{wproxy.Server{Host: "proxy.example", Port: 1080, Scheme: "socks5"}, "socks5://proxy.example:1080"},
	}
	for _, tc := range tests {
		if got := proxyKeyForSupervisor(tc.server); got != tc.want {
			t.Fatalf("%#v key = %q, want %q", tc.server, got, tc.want)
		}
	}
}
