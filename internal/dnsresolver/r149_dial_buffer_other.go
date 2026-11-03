//go:build !linux

package dnsresolver

import (
	"fmt"
	"net"
	"os"
)

// Do not silently claim that a Linux-only experimental socket limit has
// been configured on Windows/macOS.
func r149ConfigureResearchDialSocketBuffer(_ *net.Dialer) error {
	if os.Getenv("PXGO_R149_LINUX_PREDIAL_BUFFER_BYTES") != "" {
		return fmt.Errorf("R149 Linux pre-dial buffer experiment unsupported on this platform")
	}
	return nil
}
