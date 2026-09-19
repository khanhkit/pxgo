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
//
// The current Manager owns ticket acquisition/renewal only. It does not produce
// GSSAPI/SPNEGO tokens for an upstream HTTP proxy, so advertising it as an
// end-to-end proxy authentication mode would be misleading.
//
// Windows upstream Negotiate is a separate current-user SSPI path owned by the
// proxy package. The --kerberos flag must not be used as a selector for it.
func ValidateProxyAuthFeature(enabled bool, goos string) error {
	if !enabled {
		return nil
	}

	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return fmt.Errorf(
			"%w: --kerberos is not a Windows SSPI selector; omit --kerberos and explicit upstream credentials to allow current-user SSPI Negotiate",
			ErrProxyAuthUnsupported,
		)
	}

	return fmt.Errorf(
		"%w on %s: --kerberos currently manages Kerberos tickets only; no GSSAPI/SPNEGO upstream proxy-auth consumer is implemented",
		ErrProxyAuthUnsupported,
		strings.TrimSpace(goos),
	)
}
