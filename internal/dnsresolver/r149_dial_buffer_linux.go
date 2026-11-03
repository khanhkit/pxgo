//go:build linux

package dnsresolver

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// R149 disposable research hook ONLY. Set native TCP buffer caps before
// connect() even when a hostname is resolved via explicit DNS/DoH policy.
// No system-global setting and no effect unless the opt-in env is present.
func r149ConfigureResearchDialSocketBuffer(d *net.Dialer) error {
	raw := os.Getenv("PXGO_R149_LINUX_PREDIAL_BUFFER_BYTES")
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 4096 || n > 1<<20 {
		return fmt.Errorf("R149 pre-dial socket buffer must be 4096..1048576 bytes")
	}
	d.Control = func(network, address string, c syscall.RawConn) error {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil
		}
		var inner error
		err := c.Control(func(fd uintptr) {
			inner = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, n)
			if inner != nil {
				return
			}
			inner = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, n)
		})
		if err != nil {
			return err
		}
		return inner
	}
	return nil
}
