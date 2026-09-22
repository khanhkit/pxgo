//go:build windows

package proxy

import (
	"fmt"
	"strings"

	"github.com/alexbrainman/sspi/negotiate"
	"github.com/alexbrainman/sspi/ntlm"
	"github.com/khanhkit/pxgo/internal/config"
)

type ntlmSSPIContext struct {
	ctx *ntlm.ClientContext
}

func (c *ntlmSSPIContext) Update(token []byte) (bool, []byte, error) {
	output, err := c.ctx.Update(token)
	if err != nil {
		return false, nil, err
	}
	return true, output, nil
}

func (c *ntlmSSPIContext) Release() error {
	return c.ctx.Release()
}

// newSSPISession creates one native SSPI session for the selected proxy
// challenge. The returned session exclusively owns both credentials and context.
func newSSPISession(challenge, proxyHost string) (authSession, error) {
	scheme := authSchemeFromChallenge(challenge)
	switch {
	case strings.EqualFold(scheme, authNTLM):
		cred, err := ntlm.AcquireCurrentUserCredentials()
		if err != nil {
			return nil, fmt.Errorf("acquire NTLM credentials: %w", err)
		}
		ctx, token, err := ntlm.NewClientContext(cred)
		if err != nil {
			_ = cred.Release()
			return nil, fmt.Errorf("create NTLM client context: %w", err)
		}
		return newManagedSSPISession(authNTLM, token, cred, &ntlmSSPIContext{ctx: ctx}), nil

	case strings.EqualFold(scheme, authNegotiate):
		target := proxySPN(proxyHost)
		if target == "" {
			return nil, fmt.Errorf("create Negotiate client context: empty upstream proxy host")
		}
		cred, err := negotiate.AcquireCurrentUserCredentials()
		if err != nil {
			return nil, fmt.Errorf("acquire Negotiate credentials: %w", err)
		}
		ctx, token, err := negotiate.NewClientContext(cred, target)
		if err != nil {
			_ = cred.Release()
			return nil, fmt.Errorf("create Negotiate client context for %s: %w", target, err)
		}
		return newManagedSSPISession(authSchemeNeg, token, cred, ctx), nil

	default:
		return nil, fmt.Errorf("unsupported SSPI proxy challenge scheme %q", scheme)
	}
}

// isWindowsSSPICandidate returns true when the current configuration has no
// explicit credentials and the server challenge is a connection-oriented scheme
// (NTLM or Negotiate) that SSPI can satisfy transparently.
func isWindowsSSPICandidate(cfg config.Config, challenge string) bool {
	if cfg.Username != "" || cfg.Password != "" {
		return false
	}
	return isConnectionAuth(authSchemeFromChallenge(challenge))
}
