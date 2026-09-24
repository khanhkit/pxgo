package kerberos

import (
	"errors"
	"fmt"
	"strings"
)

// ErrProxyAuthUnsupported means the user-facing --kerberos mode cannot
// provide end-to-end upstream proxy authentication on the selected platform.
var ErrProxyAuthUnsupported = errors.New("kerberos upstream proxy authentication is unsupported")

// ValidateProxyAuthFeature validates the user-facing --kerberos feature contract.
// Linux and macOS consume the Manager-owned FILE ccache through the pure-Go
// GSS-API/SPNEGO implementation. Windows transparent Negotiate remains owned by
// the current-user SSPI path and --kerberos is deliberately not its selector.
func ValidateProxyAuthFeature(enabled bool, goos string) error {
	if !enabled {
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "linux", "darwin":
		return nil
	case "windows":
		return fmt.Errorf(
			"%w: --kerberos is not a Windows SSPI selector; omit --kerberos and explicit upstream credentials to allow current-user SSPI Negotiate",
			ErrProxyAuthUnsupported,
		)
	default:
		return fmt.Errorf(
			"%w on %s: upstream Kerberos SPNEGO is currently supported on Linux and macOS",
			ErrProxyAuthUnsupported,
			strings.TrimSpace(goos),
		)
	}
}
