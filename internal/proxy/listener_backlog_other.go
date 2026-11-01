//go:build !windows

package proxy

import "net"

func listenProxyTCP(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
