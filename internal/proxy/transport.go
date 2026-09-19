package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pavelsimo/pxgo/internal/wproxy"
)

// maxCachedTransports bounds the transport cache; a PAC file can emit an
// unbounded set of distinct proxies over time.
const maxCachedTransports = 64

type socksDestinationError struct {
	version int
	code    byte
}

func (e *socksDestinationError) Error() string {
	return fmt.Sprintf("SOCKS%d connect failed with code %d", e.version, e.code)
}

func (s *Server) transportCache() *boundedTransportCache {
	if s.transports != nil {
		return s.transports
	}
	// A few focused unit tests construct Server values directly instead of
	// calling New. Serialize lazy cache creation on stateMu so those zero-value
	// servers keep the same strict cache ownership contract.
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.transports == nil {
		s.transports = newBoundedTransportCache(maxCachedTransports)
	}
	return s.transports
}

func proxyTransportRouteKey(p wproxy.Server) string {
	if p == wproxy.Direct {
		return "direct"
	}
	return strings.ToLower(strings.TrimSpace(proxyScheme(p))) + "://" +
		net.JoinHostPort(strings.ToLower(strings.TrimSpace(p.Host)), strconv.Itoa(p.Port))
}

func (s *Server) httpTransportForProxy(p wproxy.Server) *http.Transport {
	key := "route|" + proxyTransportRouteKey(p)
	entry := s.transportCache().getOrCreate(key, func() *transportCacheEntry {
		return &transportCacheEntry{
			transport: s.newHTTPTransport(p),
			routeKey:  proxyTransportRouteKey(p),
		}
	})
	return entry.transport
}

func (s *Server) newHTTPTransport(p wproxy.Server) *http.Transport {
	timeout := time.Duration(s.cfg.SockTimeout * float64(time.Second))
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   timeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
	}
	if p == wproxy.Direct {
		return transport
	}
	scheme := proxyScheme(p)
	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	if strings.HasPrefix(scheme, "socks") {
		transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
			return dialSOCKSProxy(ctx, scheme, addr, target, timeout)
		}
	} else {
		transport.Proxy = http.ProxyURL(&url.URL{Scheme: scheme, Host: addr})
	}
	return transport
}

func (s *Server) clearTransports() {
	if s.transports != nil {
		s.transports.clear()
	}
}

func (s *Server) cachedTransportCount() int {
	if s.transports == nil {
		return 0
	}
	return s.transports.len()
}

func connectionAuthTransportKey(p wproxy.Server, identity, scheme string) string {
	return "auth|" + strings.ToUpper(strings.TrimSpace(scheme)) + "|" + identity + "|" + proxyTransportRouteKey(p)
}

func (s *Server) cachedConnectionAuthTransport(p wproxy.Server, identity string) (*transportCacheEntry, bool) {
	if identity == "" || s.transports == nil {
		return nil, false
	}
	for _, scheme := range upstreamAuthModes(effectiveUpstreamAuth(s.cfg)) {
		if !isConnectionAuth(scheme) {
			continue
		}
		if entry, ok := s.transports.get(connectionAuthTransportKey(p, identity, scheme)); ok {
			return entry, true
		}
	}
	return nil, false
}

func (s *Server) connectionAuthTransportForChallenge(p wproxy.Server, identity, scheme string, base *http.Transport) (*transportCacheEntry, string) {
	key := connectionAuthTransportKey(p, identity, scheme)
	entry := s.transportCache().getOrCreate(key, func() *transportCacheEntry {
		transport := base.Clone()
		transport.MaxConnsPerHost = 1
		transport.MaxIdleConnsPerHost = 1
		return &transportCacheEntry{
			transport: transport,
			scheme:    strings.ToUpper(strings.TrimSpace(scheme)),
			identity:  identity,
			routeKey:  proxyTransportRouteKey(p),
		}
	})
	return entry, key
}

func (s *Server) removeCachedTransport(key string, entry *transportCacheEntry) {
	if s.transports != nil && key != "" {
		s.transports.remove(key, entry)
	}
}

func proxyScheme(server wproxy.Server) string {
	if server.Scheme == "" {
		return httpScheme
	}
	return server.Scheme
}

func dialSOCKSProxy(ctx context.Context, scheme, proxyAddr, target string, timeout time.Duration) (net.Conn, error) {
	switch strings.ToLower(scheme) {
	case "socks4", "socks4a":
		return dialSOCKS4(ctx, proxyAddr, target, timeout)
	default:
		return dialSOCKS5(ctx, proxyAddr, target, timeout)
	}
}

func dialSOCKS5(ctx context.Context, proxyAddr, target string, timeout time.Duration) (result net.Conn, retErr error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	closeOnErr := true
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer func() {
		stopCancel()
		if retErr != nil && ctx.Err() != nil {
			retErr = ctx.Err()
		}
		if closeOnErr {
			_ = conn.Close()
		}
	}()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return nil, err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return nil, err
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		return nil, fmt.Errorf("SOCKS5 proxy rejected no-auth method")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	port16, err := socksPort(port)
	if err != nil {
		return nil, err
	}
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, 0x01)
			req = append(req, ip4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("SOCKS5 target host too long")
		}
		hostLen, err := socksHostLen(host)
		if err != nil {
			return nil, err
		}
		req = append(req, 0x03, hostLen)
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, port16)
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	if header[0] != 0x05 || header[1] != 0x00 {
		return nil, &socksDestinationError{version: 5, code: header[1]}
	}
	var skip int
	switch header[3] {
	case 0x01:
		skip = 4
	case 0x03:
		lenb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenb); err != nil {
			return nil, err
		}
		skip = int(lenb[0])
	case 0x04:
		skip = 16
	default:
		return nil, fmt.Errorf("SOCKS5 bad address type %d", header[3])
	}
	if _, err := io.ReadFull(conn, make([]byte, skip+2)); err != nil {
		return nil, err
	}
	stopCancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	closeOnErr = false
	return conn, nil
}

func dialSOCKS4(ctx context.Context, proxyAddr, target string, timeout time.Duration) (result net.Conn, retErr error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	closeOnErr := true
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer func() {
		stopCancel()
		if retErr != nil && ctx.Err() != nil {
			retErr = ctx.Err()
		}
		if closeOnErr {
			_ = conn.Close()
		}
	}()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	port16, err := socksPort(port)
	if err != nil {
		return nil, err
	}
	req := []byte{0x04, 0x01}
	req = binary.BigEndian.AppendUint16(req, port16)
	if ip := net.ParseIP(host).To4(); ip != nil {
		req = append(req, ip...)
		req = append(req, 0x00)
	} else {
		req = append(req, 0, 0, 0, 1, 0x00)
		req = append(req, []byte(host)...)
		req = append(req, 0x00)
	}
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return nil, err
	}
	if reply[1] != 0x5a {
		return nil, &socksDestinationError{version: 4, code: reply[1]}
	}
	stopCancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	closeOnErr = false
	return conn, nil
}

func socksPort(port int) (uint16, error) {
	if port < 0 || port > 65535 {
		return 0, fmt.Errorf("SOCKS target port out of range: %d", port)
	}
	return uint16(port), nil
}

func socksHostLen(host string) (byte, error) {
	if len(host) > 255 {
		return 0, fmt.Errorf("SOCKS5 target host too long")
	}
	// #nosec G115 -- length is bounded above before conversion.
	return byte(len(host)), nil
}
