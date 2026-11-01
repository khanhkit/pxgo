//go:build windows

package proxy

import (
	"net"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Research-only: a failed Windows Bind must fail CLOSED instead of sharing
// the occupied proxy port, and the raw WinSock handle must close once.
func TestR130WindowsBacklogPortCollisionAndHandleCleanup(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "4096")
	owner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	address := owner.Addr().String()
	current, err := windows.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	countHandles := func() uint32 {
		t.Helper()
		var count uint32
		ok, _, _ := capacityGetProcessHandleCount.Call(uintptr(current), uintptr(unsafe.Pointer(&count)))
		if ok == 0 {
			t.Fatal("GetProcessHandleCount unavailable")
		}
		return count
	}
	runtime.GC()
	before := countHandles()
	const trials = 256
	var deniedAddrInUse, deniedAccess int
	for i := 0; i < trials; i++ {
		ln, bindErr := listenProxyTCP(address)
		if bindErr == nil {
			_ = ln.Close()
			t.Fatalf("Windows socket port takeover: duplicate raw socket bound to occupied %s at trial=%d", address, i)
		}
		switch capacityNativeSocketErrno(bindErr) {
		case 10048: // WSAEADDRINUSE
			deniedAddrInUse++
		case 10013: // WSAEACCES; some providers deny second bind
			deniedAccess++
		default:
			t.Fatalf("unexpected WinSock bind rejection at trial=%d: %v", i, bindErr)
		}
	}
	runtime.GC()
	after := countHandles()
	if after > before+16 {
		t.Fatalf("raw WinSock handles leaked across failed Bind: before=%d after=%d", before, after)
	}
	t.Logf("R130_WINDOWS_BIND_RESULT attempted=%d rejected=%d addr_in_use=%d access_denied=%d before_handles=%d after_handles=%d port_takeover=false", trials, deniedAddrInUse+deniedAccess, deniedAddrInUse, deniedAccess, before, after)
}

func TestR130WindowsBacklogInvalidConfigurationRejectsBeforeBind(t *testing.T) {
	cases := []string{"0", "1", "-4096", "65536", "8192junk", " 4096 ", ""}
	for _, value := range cases {
		if value == "" { // empty means the documented normal Go listener
			continue
		}
		t.Run(value, func(t *testing.T) {
			t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", value)
			ln, err := listenProxyTCP("127.0.0.1:0")
			if err == nil {
				_ = ln.Close()
				t.Fatalf("invalid experimental backlog %q opened a socket", value)
			}
		})
	}
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "4096")
	for _, address := range []string{"[::1]:0", "localhost:0", "127.0.0.1:invalid"} {
		ln, err := listenProxyTCP(address)
		if err == nil {
			_ = ln.Close()
			t.Fatalf("experimental IPv4-only listener silently accepted %q", address)
		}
	}
	// With the opt-in variable absent, the baseline Windows net.Listen path
	// remains available for normal hostname resolution.
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "")
	ln, err := listenProxyTCP("localhost:0")
	if err != nil {
		t.Fatalf("normal default listener no longer accepts localhost: %v", err)
	}
	_ = ln.Close()
	t.Log("R130_WINDOWS_INVALID_RESULT rejected_malformed=true rejected_ipv6=true rejected_hostname_in_optin=true unchanged_default_localhost=true")
}
