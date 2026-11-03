//go:build linux

package dnsresolver

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"
)

func r149SocketBuffers(t *testing.T, c net.Conn) (int, int) {
	t.Helper()
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("expected *net.TCPConn got %T", c)
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if inner != nil {
		t.Fatal(inner)
	}
	return read, write
}

func TestR149LinuxDNSPolicyPreconnectBufferCapAllModes(t *testing.T) {
	for _, mode := range []string{"system", "custom-resolved-name"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PXGO_R149_LINUX_PREDIAL_BUFFER_BYTES", "65536")
			ln, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			dns := "system"
			address := ln.Addr().String()
			var lookup LookupFunc
			if mode == "custom-resolved-name" {
				dns = "127.0.0.1:53"
				address = net.JoinHostPort("fixture.example", fmt.Sprint(ln.Addr().(*net.TCPAddr).Port))
				lookup = func(ctx context.Context, hostname string) ([]net.IP, error) {
					if hostname != "fixture.example" {
						return nil, fmt.Errorf("unexpected lookup for %q", hostname)
					}
					return []net.IP{net.IPv4(127, 0, 0, 1)}, nil
				}
			}
			policy, err := New(dns, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			client, err := policy.DialContext(context.Background(), "tcp4", address, lookup)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			accepted, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer accepted.Close()
			read, write := r149SocketBuffers(t, client)
			if read != 131072 || write != 131072 {
				t.Fatalf("R149_DNS_PREDIAL_CAP_NOT_APPLIED mode=%s read=%d write=%d", mode, read, write)
			}
			t.Logf("R149_DNS_PREDIAL_RESULT mode=%s requested=65536 linux_read=%d linux_write=%d applied_before_connect=true", mode, read, write)
		})
	}
}

func TestR149LinuxDNSPolicyInvalidCapFailsBeforeNetwork(t *testing.T) {
	t.Setenv("PXGO_R149_LINUX_PREDIAL_BUFFER_BYTES", "invalid")
	policy, err := New("system", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := policy.DialContext(context.Background(), "tcp4", "127.0.0.1:9", nil)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("unexpected successful dial")
	}
	if err == nil || err.Error() != "R149 pre-dial socket buffer must be 4096..1048576 bytes" {
		t.Fatalf("must reject invalid research cap before any network activity: %v", err)
	}
}
