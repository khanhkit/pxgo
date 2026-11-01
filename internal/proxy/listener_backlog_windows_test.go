//go:build windows

package proxy

import (
	"net"
	"testing"
	"time"
)

func TestExperimentalWindowsBacklogListenerSmoke(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "4096")
	for i := 0; i < 3; i++ {
		ln, err := listenProxyTCP("127.0.0.1:0")
		if err != nil {
			t.Fatalf("experimental Windows listener open %d: %v", i, err)
		}
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 3*time.Second)
		if err != nil {
			_ = ln.Close()
			t.Fatalf("experimental Windows backlog dial %d: %v", i, err)
		}
		_ = conn.Close()
		if err := ln.Close(); err != nil {
			t.Fatalf("experimental listener close %d: %v", i, err)
		}
	}
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "1")
	if ln, err := listenProxyTCP("127.0.0.1:0"); err == nil {
		_ = ln.Close()
		t.Fatal("unsupported backlog 1 accepted")
	}
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "4096")
	if ln, err := listenProxyTCP("[::1]:0"); err == nil {
		_ = ln.Close()
		t.Fatal("IPv6 accepted with experimental IPv4-only backlog")
	}
}
