package proxy

import (
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/kerberos"
)

type kerberosAuthSession struct {
	mu     sync.Mutex
	token  []byte
	sent   bool
	closed bool
}

func newKerberosAuthSession(manager *kerberos.Manager, proxyHost string) (authSession, error) {
	if manager == nil {
		return nil, errors.New("kerberos ticket manager is unavailable")
	}
	token, err := kerberosTokenFactory(manager, proxyHost)
	if err != nil {
		return nil, err
	}
	return &kerberosAuthSession{token: append([]byte(nil), token...)}, nil
}

func (s *kerberosAuthSession) Negotiate() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("kerberos SPNEGO session is closed")
	}
	if s.sent {
		return "", errors.New("kerberos SPNEGO initial token already sent")
	}
	if len(s.token) == 0 {
		return "", errors.New("kerberos SPNEGO token is empty")
	}
	s.sent = true
	return authSchemeNeg + " " + base64.StdEncoding.EncodeToString(s.token), nil
}

func (s *kerberosAuthSession) Authenticate(challengeHeader string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("kerberos SPNEGO session is closed")
	}
	if !s.sent {
		return "", errors.New("kerberos SPNEGO initial token has not been sent")
	}
	if !strings.EqualFold(authSchemeFromChallenge(challengeHeader), authNegotiate) {
		return "", fmt.Errorf("kerberos SPNEGO challenge scheme %q is not Negotiate", authSchemeFromChallenge(challengeHeader))
	}
	// The Manager-owned ccache path produces a complete Kerberos AP-REQ in the
	// initial NegTokenInit. A second 407 means the proxy rejected that exchange;
	// do not silently downgrade it to NTLM or replay credentials.
	return "", errors.New("kerberos SPNEGO proxy authentication was rejected after the initial token")
}

func (s *kerberosAuthSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	clear(s.token)
	s.token = nil
	s.closed = true
	return nil
}

func isUnixKerberosCandidate(cfg config.Config, challenge string) bool {
	if !cfg.Kerberos || runtime.GOOS == goosWindows {
		return false
	}
	return strings.EqualFold(authSchemeFromChallenge(challenge), authNegotiate)
}

var (
	kerberosSessionCandidate = isUnixKerberosCandidate
	kerberosSessionFactory   = newKerberosAuthSession
	kerberosTokenFactory     = func(manager *kerberos.Manager, proxyHost string) ([]byte, error) {
		return manager.SPNEGOToken(proxyHost)
	}
)
