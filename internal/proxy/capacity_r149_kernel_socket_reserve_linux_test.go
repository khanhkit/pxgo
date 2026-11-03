//go:build linux

package proxy

import (
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// Linux SO_RESERVE_MEM (socket option 73) is a REAL memcg forward allocation
// reservation, not a cap on future TCP memory and not a way to precharge all
// SO_SNDBUF+SO_RCVBUF. RESEARCH ONLY; never enabled in production by this test.
func TestR149LinuxRealMemcgSockReservePrecharge(t *testing.T) {
	if os.Getenv("PXGO_R149_RESERVE_CGROUP") != "1" {
		t.Skip("requires disposable finite 384MiB cgroup")
	}
	limit := readR128Metric(t, "memory.max")
	if limit != 384<<20 {
		t.Fatalf("must run in disposable 384MiB cgroup, got %d", limit)
	}
	const pairs = 16
	const requested = 16 << 10
	const soReserveMem = 73
	sockBefore, currentBefore, oomBefore := r147MemcgSnapshot(t)
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	servers := make([]*net.TCPConn, 0, pairs)
	clients := make([]*net.TCPConn, 0, pairs)
	defer func() {
		for _, s := range servers {
			_ = s.Close()
		}
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	var supported bool
	for i := 0; i < pairs; i++ {
		client, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
		if err != nil {
			t.Fatal(err)
		}
		server, err := ln.AcceptTCP()
		if err != nil {
			_ = client.Close()
			t.Fatal(err)
		}
		clients = append(clients, client)
		servers = append(servers, server)
		raw, err := server.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var setErr error
		var got int
		err = raw.Control(func(fd uintptr) {
			setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReserveMem, requested)
			if setErr == nil {
				got, setErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, soReserveMem)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if setErr == syscall.ENOPROTOOPT || setErr == syscall.EOPNOTSUPP {
			t.Skipf("SO_RESERVE_MEM not enabled on kernel/cgroup: %v", setErr)
		}
		if setErr != nil {
			t.Fatalf("SO_RESERVE_MEM support/charge failed: %v", setErr)
		}
		if got != requested {
			t.Fatalf("SO_RESERVE_MEM returned got=%d want=%d", got, requested)
		}
		supported = true
	}
	time.Sleep(20 * time.Millisecond)
	sockActive, currentActive, oomActive := r147MemcgSnapshot(t)
	if oomActive != oomBefore {
		t.Fatal("OOM while reserving controlled TCP buffers")
	}
	// Closing socket releases its socket-reserved memory eventually, but no
	// claim is made about charge timing or all other memcg memory categories.
	for _, s := range servers {
		_ = s.Close()
	}
	for _, c := range clients {
		_ = c.Close()
	}
	time.Sleep(80 * time.Millisecond)
	sockAfter, currentAfter, oomAfter := r147MemcgSnapshot(t)
	if oomAfter != oomBefore {
		t.Fatal("OOM after closing sockets")
	}
	t.Logf("R149_SO_RESERVE_MEM_RESULT memory_max=%d sockets=%d per_socket_precharge_bytes=%d reserve_supported=%t sock_before=%d sock_active=%d sock_after=%d sock_peak_delta=%d current_before=%d current_active=%d current_after=%d oom_kill_delta=0", limit, pairs, requested, supported, sockBefore, sockActive, sockAfter, int64(sockActive)-int64(sockBefore), currentBefore, currentActive, currentAfter)
}
