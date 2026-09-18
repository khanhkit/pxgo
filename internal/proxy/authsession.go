package proxy

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// authSession manages a stateful proxy authentication exchange.
// Windows SSPI owns credential/context resources until terminal completion/error.
// Non-SSPI auth continues to use upstreamProxyAuthHeader directly.
type authSession interface {
	// Negotiate returns the initial Proxy-Authorization header before a challenge token arrives.
	Negotiate() (string, error)
	// Authenticate returns the Proxy-Authorization header in response to a server challenge header.
	Authenticate(challengeHeader string) (string, error)
	// Close releases all native/session resources. It must be safe to call more than once.
	Close() error
}

type sspiCredential interface {
	Release() error
}

type sspiClientContext interface {
	Update(token []byte) (authCompleted bool, outputToken []byte, err error)
	Release() error
}

type sspiAuthSession struct {
	mu           sync.Mutex
	closeOnce    sync.Once
	closeErr     error
	credential   sspiCredential
	ctx          sspiClientContext
	scheme       string
	initialToken []byte
	initialSent  bool
	complete     bool
	closed       bool
}

func newManagedSSPISession(scheme string, initialToken []byte, credential sspiCredential, ctx sspiClientContext) *sspiAuthSession {
	canonical := authSchemeNeg
	if strings.EqualFold(scheme, authNTLM) {
		canonical = authNTLM
	}
	return &sspiAuthSession{
		credential:   credential,
		ctx:          ctx,
		scheme:       canonical,
		initialToken: append([]byte(nil), initialToken...),
	}
}

func (s *sspiAuthSession) Negotiate() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("SSPI: session is closed")
	}
	if s.initialSent {
		return "", errors.New("SSPI: initial token already sent")
	}
	if len(s.initialToken) == 0 {
		return "", errors.New("SSPI: no initial token available")
	}
	s.initialSent = true
	return s.scheme + " " + base64.StdEncoding.EncodeToString(s.initialToken), nil
}

func (s *sspiAuthSession) Authenticate(challengeHeader string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("SSPI: session is closed")
	}
	if !s.initialSent {
		return "", errors.New("SSPI: initial token has not been sent")
	}
	if s.complete {
		return "", errors.New("SSPI: authentication is already complete")
	}

	scheme, tokenStr, found := strings.Cut(strings.TrimSpace(challengeHeader), " ")
	if !found || strings.TrimSpace(tokenStr) == "" {
		return "", errors.New("SSPI: challenge token is missing")
	}
	if !strings.EqualFold(scheme, s.scheme) {
		return "", fmt.Errorf("SSPI: challenge scheme %q does not match session scheme %q", scheme, s.scheme)
	}
	challenge, err := base64.StdEncoding.DecodeString(strings.TrimSpace(tokenStr))
	if err != nil {
		return "", fmt.Errorf("SSPI: decode challenge: %w", err)
	}
	completed, outputToken, err := s.ctx.Update(challenge)
	if err != nil {
		return "", fmt.Errorf("SSPI: update context: %w", err)
	}
	if len(outputToken) == 0 {
		return "", errors.New("SSPI: continuation produced no output token")
	}
	s.complete = completed
	return s.scheme + " " + base64.StdEncoding.EncodeToString(outputToken), nil
}

func (s *sspiAuthSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = true
		var errs []error
		if s.ctx != nil {
			if err := s.ctx.Release(); err != nil {
				errs = append(errs, fmt.Errorf("release SSPI context: %w", err))
			}
		}
		if s.credential != nil {
			if err := s.credential.Release(); err != nil {
				errs = append(errs, fmt.Errorf("release SSPI credentials: %w", err))
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

func proxySPN(proxyHost string) string {
	host := strings.TrimSpace(proxyHost)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if host == "" {
		return ""
	}
	return "HTTP/" + host
}

// These indirections keep the platform boundary injectable for deterministic
// orchestration tests without exporting test-only API.
var (
	sspiSessionCandidate = isWindowsSSPICandidate
	sspiSessionFactory   = newSSPISession
)
