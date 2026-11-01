//go:build windows

package proxy

import (
	"net"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"unsafe"
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

func TestExperimentalWindowsBacklogRepeatedHandleOwnership(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "4096")
	current, err := windows.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	count := func() uint32 {
		t.Helper()
		var handles uint32
		ok, _, _ := capacityGetProcessHandleCount.Call(uintptr(current), uintptr(unsafe.Pointer(&handles)))
		if ok == 0 {
			t.Fatal("GetProcessHandleCount failed")
		}
		return handles
	}
	runtime.GC()
	before := count()
	for i := 0; i < 128; i++ {
		ln, listenErr := listenProxyTCP("127.0.0.1:0")
		if listenErr != nil {
			t.Fatalf("repeated backlog listener %d: %v", i, listenErr)
		}
		conn, dialErr := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
		if dialErr != nil {
			_ = ln.Close()
			t.Fatalf("repeated backlog dial %d: %v", i, dialErr)
		}
		_ = conn.Close()
		if closeErr := ln.Close(); closeErr != nil {
			t.Fatalf("repeated backlog Close %d: %v", i, closeErr)
		}
	}
	runtime.GC()
	after := count()
	const tolerance = uint32(16)
	if after > before+tolerance {
		t.Fatalf("possible Windows backlog socket handle leak: before=%d after=%d tolerance=%d", before, after, tolerance)
	}
	t.Logf("WINDOWS_BACKLOG_HANDLE_RESULT loops=128 before=%d after=%d tolerance=%d", before, after, tolerance)
}
