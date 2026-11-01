//go:build windows

package proxy

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

// listenProxyTCP preserves Go's ordinary Windows listener unless this
// explicitly research-only socket backlog is requested. It NEVER alters
// machine-wide networking settings and remains independent of AUTO admission.
func listenProxyTCP(addr string) (net.Listener, error) {
	text := os.Getenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG")
	if text == "" {
		return net.Listen("tcp", addr)
	}
	backlog, err := strconv.Atoi(text)
	if err != nil || backlog < 200 || backlog > 65535 {
		return nil, fmt.Errorf("experimental Windows TCP backlog must be 200..65535")
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return nil, fmt.Errorf("experimental Windows TCP backlog supports only explicit IPv4 addresses")
	}
	// A larger pre-accept WinSock queue is outside AUTO's physical permits.
	// Fail closed on externally reachable listeners without a separate,
	// explicit acknowledgment of that unauthenticated TCP exposure.
	approval := os.Getenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG_ALLOW_NON_LOOPBACK")
	if approval != "" && approval != "1" {
		return nil, fmt.Errorf("Windows backlog non-loopback opt-in must be 1 when set")
	}
	if !ip.IsLoopback() && approval != "1" {
		return nil, fmt.Errorf("experimental Windows TCP backlog on non-loopback IPv4 requires PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG_ALLOW_NON_LOOPBACK=1")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid Windows TCP listen port %q", portText)
	}
	var ip4 [4]byte
	copy(ip4[:], ip)
	// Go net.Listen uses SOMAXCONN on Windows. Its effective backlog is
	// provider-defined (about 200 on the observed native runner). A negative
	// backlog is Microsoft's documented SOMAXCONN_HINT(N) to request N.
	socket, err := windows.WSASocket(windows.AF_INET, windows.SOCK_STREAM,
		windows.IPPROTO_TCP, nil, 0,
		windows.WSA_FLAG_OVERLAPPED|windows.WSA_FLAG_NO_HANDLE_INHERIT)
	if err != nil {
		return nil, fmt.Errorf("Windows backlog socket: %w", err)
	}
	// The raw Winsock handle is owned here only until os.NewFile adopts it.
	// Closing the raw socket after file.Close would double-close a reused handle.
	rawOwned := true
	defer func() {
		if rawOwned {
			_ = windows.Closesocket(socket)
		}
	}()
	if err := windows.Bind(socket, &windows.SockaddrInet4{Port: port, Addr: ip4}); err != nil {
		return nil, fmt.Errorf("Windows backlog bind: %w", err)
	}
	if err := windows.Listen(socket, -backlog); err != nil {
		return nil, fmt.Errorf("Windows SOMAXCONN_HINT(%d) listen: %w", backlog, err)
	}
	// net.FileListener duplicates the listening socket, so the returned
	// listener owns a distinct handle. The original is closed on every path.
	file := os.NewFile(uintptr(socket), "pxgo-windows-backlog-listener")
	if file == nil {
		return nil, fmt.Errorf("cannot wrap Windows backlog listener handle")
	}
	rawOwned = false // os.File now owns the original Winsock socket.
	listener, convertErr := net.FileListener(file)
	closeErr := file.Close()
	if convertErr != nil {
		return nil, fmt.Errorf("Windows backlog FileListener: %w", convertErr)
	}
	if closeErr != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("Windows backlog original socket close: %w", closeErr)
	}
	return listener, nil
}
