//go:build windows

package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

// Test-only WinSock pre-bind/pre-connect buffer configuration. This probes
// inherited accepted-socket limits, unlike the previous post-Accept test.
// No changes to production PxGo socket options or Windows system settings.
func r149SetWinBuffersOnRawSocket(raw syscall.RawConn, buf int) error {
	var inner error
	err := raw.Control(func(fd uintptr) {
		if inner = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, buf); inner != nil {
			return
		}
		inner = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, buf)
	})
	if err != nil {
		return err
	}
	return inner
}

func r149WinSocketBufferValues(c *net.TCPConn) (int, int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var read, write int
	var inner error
	err = raw.Control(func(fd uintptr) {
		read, inner = syscall.GetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		if inner != nil {
			return
		}
		write, inner = syscall.GetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	})
	if err != nil {
		return 0, 0, err
	}
	return read, write, inner
}

func TestR149WindowsListenerAndDialerPreConnectBufferCaps(t *testing.T) {
	const requested = 64 << 10
	var listenerControl, dialControl bool
	lc := net.ListenConfig{Control: func(network, address string, raw syscall.RawConn) error {
		if network != "tcp4" {
			return fmt.Errorf("unexpected network %s", network)
		}
		listenerControl = true
		return r149SetWinBuffersOnRawSocket(raw, requested)
	}}
	listener, err := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dialer := net.Dialer{Timeout: time.Second, Control: func(network, address string, raw syscall.RawConn) error {
		dialControl = true
		return r149SetWinBuffersOnRawSocket(raw, requested)
	}}
	clientRaw, err := dialer.DialContext(context.Background(), "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := clientRaw.(*net.TCPConn)
	defer client.Close()
	server, err := listener.(*net.TCPListener).AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if !listenerControl || !dialControl {
		t.Fatalf("control callbacks listener=%v dialer=%v", listenerControl, dialControl)
	}
	cr, cw, err := r149WinSocketBufferValues(client)
	if err != nil {
		t.Fatal(err)
	}
	sr, sw, err := r149WinSocketBufferValues(server)
	if err != nil {
		t.Fatal(err)
	}
	// Log exact values even when WinSock inheritance is unsupported. Do not
	// silently assume Unix socket option behavior or accept unwanted defaults.
	t.Logf("R149_WINDOWS_PREACCEPT_BUFFERS_OBSERVATION requested=%d client_read=%d client_write=%d accepted_read=%d accepted_write=%d", requested, cr, cw, sr, sw)
	if cr > requested*4 || cw > requested*4 || sr > requested*4 || sw > requested*4 {
		t.Fatalf("R149_WINDOWS_PREACCEPT_INHERITANCE_UNVERIFIED: client=(%d,%d) accepted=(%d,%d)", cr, cw, sr, sw)
	}
	if _, err = client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err = io.ReadFull(server, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "ping" {
		t.Fatalf("unexpected data %q", b)
	}
	t.Logf("R149_WINDOWS_PREACCEPT_BUFFERS_RESULT listener_raw_control=true dial_raw_control=true socket_inheritance=true payload_ok=true")
}
