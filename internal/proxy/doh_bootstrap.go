package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/khanhkit/pxgo/internal/wproxy"
)

// Prevent DoH upstream-proxy recursion at the actual dial boundary. Resolve
// proxy hostnames independently of DoH, reject local-interface destinations on
// the PxGo listener port, then dial the checked IP rather than resolving again.
// A failed candidate may still fall back to the next candidate.
func newDoHBootstrapSafeDialer(proxies []wproxy.Server, listenerPort int, base contextDialFunc) contextDialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, portText, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return nil, err
		}
		isProxy := false
		for _, candidate := range proxies {
			if candidate != wproxy.Direct && strings.EqualFold(candidate.Host, host) && candidate.Port == port {
				isProxy = true
				break
			}
		}
		if !isProxy {
			return base(ctx, network, address)
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("DoH upstream proxy OS-DNS bootstrap: %w", err)
		}
		if len(ips) == 0 {
			return nil, errors.New("DoH upstream proxy OS-DNS bootstrap: no addresses")
		}
		if port == listenerPort {
			local, err := net.InterfaceAddrs()
			if err != nil {
				return nil, fmt.Errorf("verify local interface addresses: %w", err)
			}
			for _, resolved := range ips {
				if resolved.IP.IsLoopback() || resolved.IP.IsUnspecified() || isLocalInterfaceIP(resolved.IP, local) {
					return nil, errors.New("DoH bootstrap proxy resolves to PxGo listener address")
				}
			}
		}
		var dialErr error
		for _, resolved := range ips {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			pinned := net.JoinHostPort(resolved.String(), portText)
			conn, err := base(ctx, network, pinned)
			if err == nil {
				return conn, nil
			}
			dialErr = errors.Join(dialErr, err)
		}
		return nil, fmt.Errorf("connect DoH upstream proxy at resolved addresses: %w", dialErr)
	}
}

func isLocalInterfaceIP(ip net.IP, local []net.Addr) bool {
	for _, addr := range local {
		switch a := addr.(type) {
		case *net.IPNet:
			if ip.Equal(a.IP) {
				return true
			}
		case *net.IPAddr:
			if ip.Equal(a.IP) {
				return true
			}
		}
	}
	return false
}

// TLS can read bytes already buffered after the upstream's CONNECT response.
type dohBufferedConn struct {
	net.Conn
	pending *bytes.Reader
}

func (c *dohBufferedConn) Read(p []byte) (int, error) {
	if c.pending.Len() > 0 {
		return c.pending.Read(p)
	}
	return c.Conn.Read(p)
}

// Ensure interface compliance without changing net.Conn's other methods.
var _ io.Reader = (*dohBufferedConn)(nil)
