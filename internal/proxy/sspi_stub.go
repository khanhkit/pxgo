//go:build !windows

package proxy

import (
	"errors"

	"github.com/pavelsimo/pxgo/internal/config"
)

func newSSPISession(_, _ string) (authSession, error) {
	return nil, errors.New("SSPI is only available on Windows")
}

func isWindowsSSPICandidate(_ config.Config, _ string) bool {
	return false
}
