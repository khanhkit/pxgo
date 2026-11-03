//go:build !linux && !windows

package proxy

import (
	"fmt"
	"net"
	"os"
)

// Preserve legacy listen behavior on non-Linux/non-Windows OSes.
// The experimental Linux-only prelisten flag must not silently pretend
// to constrain an unsupported platform.
func listenProxyTCP(addr string) (net.Listener, error) {
	if os.Getenv("PXGO_R149_LINUX_PRELISTEN_BUFFER_BYTES") != "" {
		return nil, fmt.Errorf("R149 Linux pre-listen cap unsupported on this OS")
	}
	return net.Listen("tcp", addr)
}
