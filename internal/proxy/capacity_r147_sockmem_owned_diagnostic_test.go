//go:build linux

package proxy

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

// R147 research only: Linux SO_MEMINFO=55 returns 9 uint32 values
// (rmem_alloc,rcvbuf,wmem_alloc,sndbuf,fwd_alloc,wmem_queued,optmem,backlog,drops).
// These values are socket diagnostics, NOT authoritative memcg charge receipts.
func r147SockMem(c net.Conn) ([9]uint32, error) {
	var values [9]uint32
	if x, ok := c.(*admissionConn); ok {
		c = x.Conn
	}
	sc, ok := c.(syscall.Conn)
	if !ok {
		return values, fmt.Errorf("no SyscallConn for %T", c)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return values, err
	}
	var ioctlErr error
	err = raw.Control(func(fd uintptr) {
		n := uint32(len(values) * 4)
		_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, uintptr(syscall.SOL_SOCKET), 55,
			uintptr(unsafe.Pointer(&values[0])), uintptr(unsafe.Pointer(&n)), 0)
		if errno != 0 {
			ioctlErr = errno
		}
		if n != uint32(len(values)*4) && ioctlErr == nil {
			ioctlErr = fmt.Errorf("SO_MEMINFO unexpected length=%d", n)
		}
	})
	if err != nil {
		return values, err
	}
	return values, ioctlErr
}

// Only call while the source-owned Server is running; shallow copy under
// tunnelMu avoids keeping the shutdown lock while reading socket diagnostics.
func r147OwnedTunnelSocketMetrics(s *Server) (int, uint64, uint64, uint64, error) {
	s.tunnelMu.Lock()
	tunnels := make([]*managedTunnel, 0, len(s.tunnels))
	for t := range s.tunnels {
		tunnels = append(tunnels, t)
	}
	s.tunnelMu.Unlock()
	var ownedCount int
	var queued, forward, limits uint64
	for _, t := range tunnels {
		up, ok := t.upstream.(net.Conn)
		if !ok {
			continue
		}
		for _, c := range []net.Conn{t.client, up} {
			v, e := r147SockMem(c)
			if e != nil {
				return ownedCount, queued, forward, limits, e
			}
			ownedCount++
			queued += uint64(v[0]) + uint64(v[5])
			forward += uint64(v[4])
			limits += uint64(v[1]) + uint64(v[3])
		}
	}
	return ownedCount, queued, forward, limits, nil
}
