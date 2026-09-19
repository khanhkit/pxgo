package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
	"github.com/pavelsimo/pxgo/internal/debug"
	"github.com/pavelsimo/pxgo/internal/supervisor"
	"github.com/pavelsimo/pxgo/internal/wproxy"
)

// connectTarget defaults the port to 443 when the CONNECT host has none,
// including bracketed IPv6 literals like "[::1]".
type upstreamConnectStatusError struct {
	StatusCode int
	Status     string
}

func (e *upstreamConnectStatusError) Error() string {
	return "upstream CONNECT failed: " + e.Status
}

func connectTarget(host string) string {
	if _, _, err := net.SplitHostPort(host); err != nil {
		return net.JoinHostPort(strings.Trim(host, "[]"), "443")
	}
	return host
}

func (s *Server) handleConnect(rw http.ResponseWriter, req *http.Request) {
	target := connectTarget(req.Host)
	debug.Dprint("CONNECT target: " + target)
	proxies, err := s.findProxyForURL("https://" + target)
	if err != nil {
		s.recoverRuntimeOutcome(supervisor.OutcomeRouteFailure, wproxy.Server{})
		debug.Dprint("CONNECT proxy lookup error: " + err.Error())
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	debug.Dprintf("CONNECT proxies: %v", proxies)
	upstream, leftover, err := s.connectWithProxyFallback(req.Context(), target, req.Header.Get("Proxy-Authorization"), proxies)
	if err != nil {
		debug.Dprint("CONNECT failed: " + err.Error())
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := rw.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(rw, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	if !s.reserveTunnel() {
		_ = upstream.Close()
		http.Error(rw, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	client, brw, err := hijacker.Hijack()
	if err != nil {
		s.cancelTunnelReservation()
		_ = upstream.Close()
		return
	}
	tunnel, ok := s.activateTunnel(client, upstream)
	if !ok {
		return
	}
	_, _ = brw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	// Bytes the upstream proxy sent behind its CONNECT response (e.g. a
	// server-speaks-first banner) belong to the tunnel; forward them.
	if len(leftover) > 0 {
		_, _ = brw.Write(leftover)
	}
	if err := brw.Flush(); err != nil {
		s.closeAndFinishTunnel(tunnel)
		return
	}
	// Bytes the client pipelined behind the CONNECT request (e.g. an eager
	// TLS ClientHello) are buffered in brw.Reader; relay reads the raw conn,
	// so forward them explicitly or they would be lost.
	if n := brw.Reader.Buffered(); n > 0 {
		pipelined, _ := brw.Peek(n)
		if _, err := upstream.Write(pipelined); err != nil {
			s.closeAndFinishTunnel(tunnel)
			return
		}
		_, _ = brw.Discard(n)
	}
	debug.Dprint("CONNECT tunnel established: " + target)
	go func() {
		defer s.finishTunnel(tunnel)
		relay(client, upstream, time.Duration(s.cfg.Idle)*time.Second)
	}()
}

func (s *Server) connectWithProxyFallback(ctx context.Context, target, incomingProxyAuth string, proxies []wproxy.Server) (net.Conn, []byte, error) {
	timeout := time.Duration(s.cfg.SockTimeout * float64(time.Second))
	dialer := &net.Dialer{Timeout: timeout}
	var lastErr error
	for _, p := range s.orderedProxyCandidates(proxyCandidates(proxies)) {
		var upstream net.Conn
		var leftover []byte
		var err error
		if p == wproxy.Direct {
			debug.Dprint("CONNECT: dialing direct to " + target)
			upstream, err = dialer.DialContext(ctx, "tcp", target) // #nosec G704 -- this proxy must dial client-requested CONNECT targets.
		} else {
			addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
			debug.Dprintf("CONNECT: dialing via %s proxy %s for %s", proxyScheme(p), addr, target)
			switch scheme := proxyScheme(p); {
			case scheme == httpsScheme:
				upstream, err = dialer.DialContext(ctx, "tcp", addr)
				if err == nil {
					tlsConn := tls.Client(upstream, &tls.Config{ServerName: p.Host})
					handshakeCtx := ctx
					cancel := func() {}
					if timeout > 0 {
						handshakeCtx, cancel = context.WithTimeout(ctx, timeout)
					}
					err = tlsConn.HandshakeContext(handshakeCtx)
					cancel()
					if err == nil {
						upstream = tlsConn
						leftover, err = s.sendUpstreamConnectBounded(ctx, upstream, target, p.Host, incomingProxyAuth, timeout)
					}
				}
			case strings.HasPrefix(scheme, "socks"):
				upstream, err = dialSOCKSProxy(ctx, scheme, addr, target, timeout)
			default:
				upstream, err = dialer.DialContext(ctx, "tcp", addr)
				if err == nil {
					leftover, err = s.sendUpstreamConnectBounded(ctx, upstream, target, p.Host, incomingProxyAuth, timeout)
				}
			}
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			s.recordRuntimeOutcome(supervisor.OutcomeSuccess, p)
			debug.Dprint("CONNECT: upstream connected to " + target)
			return upstream, leftover, nil
		}

		kind := classifyProxyTransportOutcome(ctx, p, err)
		if kind == supervisor.OutcomeAuthExhausted {
			// Existing CONNECT auth handling already asks the Kerberos owner to
			// refresh. Record the classification without scheduling a duplicate.
			s.recordRuntimeOutcome(kind, p)
		} else {
			s.recoverRuntimeOutcome(kind, p)
		}
		debug.Dprint("CONNECT: attempt failed: " + err.Error())
		if upstream != nil {
			_ = upstream.Close()
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no proxy candidates")
	}
	debug.Dprint("CONNECT: all candidates failed for " + target + ": " + lastErr.Error())
	return nil, nil, lastErr
}

func (s *Server) sendUpstreamConnectBounded(ctx context.Context, conn net.Conn, target, proxyHost, passthroughAuth string, timeout time.Duration) ([]byte, error) {
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	leftover, err := s.sendUpstreamConnect(conn, target, proxyHost, passthroughAuth)
	stopCancel()
	_ = conn.SetDeadline(time.Time{})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return leftover, err
}

func (s *Server) sendUpstreamConnect(conn net.Conn, target, proxyHost, passthroughAuth string) ([]byte, error) {
	return sendUpstreamConnectWithAuth(conn, target, proxyHost, s.cfg, "", passthroughAuth, s.forceKerberosReloadForUpstreamAuth)
}

// sendUpstreamConnectWithAuth performs the CONNECT handshake with the upstream
// proxy and returns any tunnel bytes the upstream sent behind its response
// headers (they end up in the response reader's buffer and must be forwarded
// to the client, or server-speaks-first protocols would hang).
func sendUpstreamConnectWithAuth(conn net.Conn, target, proxyHost string, cfg config.Config, challenge, passthroughAuth string, onAuthFailure func(*http.Response)) ([]byte, error) {
	// One reader for all auth retry attempts: bytes buffered behind an
	// intermediate 407 must not be stranded in a discarded reader.
	reader := bufio.NewReader(conn)
	if err := sendUpstreamConnectAttempt(conn, reader, target, proxyHost, cfg, challenge, passthroughAuth, 0, nil, onAuthFailure); err != nil {
		return nil, err
	}
	if n := reader.Buffered(); n > 0 {
		leftover := make([]byte, n)
		_, _ = io.ReadFull(reader, leftover)
		return leftover, nil
	}
	return nil, nil
}

func sendUpstreamConnectAttempt(conn net.Conn, reader *bufio.Reader, target, proxyHost string, cfg config.Config, challenge, passthroughAuth string, attempts int, session authSession, onAuthFailure func(*http.Response)) error {
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	var auth string
	if session != nil {
		var err error
		auth, err = sspiSessionAuth(session, challenge)
		if err != nil {
			return fmt.Errorf("continue SSPI CONNECT authentication: %w", err)
		}
	} else {
		auth = upstreamProxyAuthHeader(cfg, http.MethodConnect, target, challenge, passthroughAuth)
	}
	if auth != "" {
		fmt.Fprintf(&b, "Proxy-Authorization: %s\r\n", auth)
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return err
	}
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusProxyAuthRequired && attempts < 3 {
		nextChallenge := resp.Header.Get("Proxy-Authenticate")
		if selected := selectProxyAuthenticateChallenge(effectiveUpstreamAuth(cfg), resp.Header.Values("Proxy-Authenticate")); selected != "" {
			nextChallenge = selected
		}
		if err := drainUpstream407Body(resp.Body); err != nil {
			return err
		}
		nextSession := session
		createdSession := false
		if nextSession == nil && sspiSessionCandidate(cfg, nextChallenge) {
			nextSession, err = sspiSessionFactory(nextChallenge, proxyHost)
			if err != nil {
				return fmt.Errorf("start SSPI CONNECT authentication: %w", err)
			}
			createdSession = true
		}
		if nextSession != nil {
			err = sendUpstreamConnectAttempt(conn, reader, target, proxyHost, cfg, nextChallenge, passthroughAuth, attempts+1, nextSession, onAuthFailure)
			if createdSession {
				if closeErr := nextSession.Close(); closeErr != nil && err == nil {
					err = fmt.Errorf("close SSPI CONNECT session: %w", closeErr)
				}
			}
			return err
		}
		if auth := upstreamProxyAuthHeader(cfg, http.MethodConnect, target, nextChallenge, passthroughAuth); auth != "" {
			return sendUpstreamConnectAttempt(conn, reader, target, proxyHost, cfg, nextChallenge, passthroughAuth, attempts+1, nil, onAuthFailure)
		}
	}
	if resp.StatusCode/100 != 2 {
		if onAuthFailure != nil {
			onAuthFailure(resp)
		}
		_ = resp.Body.Close()
		return &upstreamConnectStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	return nil
}
