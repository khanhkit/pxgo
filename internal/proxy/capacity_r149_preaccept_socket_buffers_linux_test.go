//go:build linux

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

// Apply SO_RCVBUF / SO_SNDBUF at socket CREATION, unlike the R148 research
// harness that changed buffers only after CONNECT tunnel activation.
// This is an experimental platform contract, not a production implementation.
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

func r149NativeSocketBufferValues(c *net.TCPConn) (int, int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var read, write int
	var inner error
	err = raw.Control(func(fd uintptr) {
		read, inner = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		if inner != nil {
			return
		}
		write, inner = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	})
	if err != nil {
		return 0, 0, err
	}
	return read, write, inner
}

func TestR149LinuxListenerAndDialerPreConnectBufferCaps(t *testing.T) {
	const requested = 64 << 10
	var listenerControl, dialControl bool
	lc := net.ListenConfig{Control: func(network, address string, raw syscall.RawConn) error {
		if network != "tcp4" {
			return fmt.Errorf("unexpected network %s", network)
		}
		listenerControl = true
		return r149SetBuffersOnRawSocket(raw, requested)
	}}
	listener, err := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ld := listener.(*net.TCPListener)
	dialer := net.Dialer{Timeout: time.Second, Control: func(network, address string, raw syscall.RawConn) error {
		dialControl = true
		return r149SetBuffersOnRawSocket(raw, requested)
	}}
	clientRaw, err := dialer.DialContext(context.Background(), "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := clientRaw.(*net.TCPConn)
	defer client.Close()
	server, err := ld.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if !listenerControl || !dialControl {
		t.Fatalf("control callbacks listener=%t dialer=%t", listenerControl, dialControl)
	}
	clientRead, clientWrite, err := r149NativeSocketBufferValues(client)
	if err != nil {
		t.Fatal(err)
	}
	serverRead, serverWrite, err := r149NativeSocketBufferValues(server)
	if err != nil {
		t.Fatal(err)
	}
	// Kernel uses doubled buffer sizes for bookkeeping, as documented in
	// socket(7). This test records observed values instead of hardcoding a
	// universal bound across hosts or platforms.
	if serverRead > requested*4 || serverWrite > requested*4 || clientRead > requested*4 || clientWrite > requested*4 {
		t.Fatalf("socket cap not inherited by accepted socket: server=(%d,%d), client=(%d,%d)", serverRead, serverWrite, clientRead, clientWrite)
	}
	if _, err = client.Write([]byte("r149")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err = io.ReadFull(server, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "r149" {
		t.Fatalf("payload corrupted %q", buf)
	}
	t.Logf("R149_LINUX_PREACCEPT_BUFFERS_RESULT requested=%d listener_raw_control=true dial_raw_control=true client_read=%d client_write=%d accepted_read=%d accepted_write=%d pre_accept_inheritance=true payload_ok=true", requested, clientRead, clientWrite, serverRead, serverWrite)
}
