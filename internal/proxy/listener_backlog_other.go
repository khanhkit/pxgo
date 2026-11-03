//go:build !windows

package proxy

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// Temporary R149 research path only. The real PxGo default is untouched.
// If explicitly opted in, set buffer options on the listening socket before
// bind/listen; accepted sockets inherit the options at native kernel accept.
func r149SetBuffersOnRawSocket(raw syscall.RawConn, buf int) error {
	var inner error
	err := raw.Control(func(fd uintptr) {
		if inner = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, buf); inner != nil {
			return
		}
		inner = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, buf)
	})
	if err != nil {
		return err
	}
	return inner
}

func listenProxyTCP(addr string) (net.Listener, error) {
	raw := os.Getenv("PXGO_R149_LINUX_PRELISTEN_BUFFER_BYTES")
	if raw == "" {
		return net.Listen("tcp", addr)
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 4096 || n > 1<<20 {
		return nil, fmt.Errorf("R149 pre-listen socket buffer must be 4096..1048576 bytes")
	}
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		return r149SetBuffersOnRawSocket(c, n)
	}}
	return lc.Listen(context.Background(), "tcp", addr)
}
