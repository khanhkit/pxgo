package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/Azure/go-ntlmssp"
	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/debug"
	"github.com/khanhkit/pxgo/internal/wproxy"
)

func (s *Server) retryHTTPProxyAuth(transport *http.Transport, req *http.Request, u *url.URL, body *replayableBody, targetURL, proxyHost string, resp *http.Response) (*http.Response, error) {
	return s.retryHTTPProxyAuthWithPool(transport, req, u, body, targetURL, "", proxyHost, wproxy.Direct, "", nil, false, resp)
}

func (s *Server) retryHTTPProxyAuthWithPool(transport *http.Transport, req *http.Request, u *url.URL, body *replayableBody, targetURL, passthroughAuth, proxyHost string, candidate wproxy.Server, authIdentity string, pooled *transportCacheEntry, pooledAlreadyLocked bool, resp *http.Response) (*http.Response, error) {
	return s.retryHTTPProxyAuthWithPoolConfig(transport, req, u, body, targetURL, passthroughAuth, proxyHost, candidate, authIdentity, pooled, pooledAlreadyLocked, resp, s.upstreamAuthConfig(proxyHost), true)
}

func (s *Server) retryHTTPProxyAuthWithPoolConfig(transport *http.Transport, req *http.Request, u *url.URL, body *replayableBody, targetURL, passthroughAuth, proxyHost string, candidate wproxy.Server, authIdentity string, pooled *transportCacheEntry, pooledAlreadyLocked bool, resp *http.Response, cfg config.Config, allowPrompt bool) (retResp *http.Response, retErr error) {
	if s.skipUpstreamAuthRetry(req, body, pooled, pooledAlreadyLocked, resp) {
		return resp, nil
	}

	var session authSession
	var mechanism authMechanismObservation
	var authAttempted bool
	var usedSSPI bool
	var ephemeralPinned *http.Transport
	activePooled := pooled
	var lockedPooled *transportCacheEntry
	lockPooled := func(entry *transportCacheEntry) {
		if lockedPooled == entry {
			return
		}
		if lockedPooled != nil {
			lockedPooled.authMu.Unlock()
		}
		lockedPooled = entry
		if lockedPooled != nil {
			lockedPooled.authMu.Lock()
		}
	}
	if activePooled != nil {
		// The caller received this 407 through the cached transport, so its
		// previously authenticated socket is no longer authoritative.
		if pooledAlreadyLocked {
			lockedPooled = activePooled
		} else {
			lockPooled(activePooled)
		}
		activePooled.authenticated = false
	}

	removePooled := func() {
		if activePooled == nil {
			return
		}
		s.removeCachedTransport(activePooled.key, activePooled)
	}
	closeEphemeral := func() {
		if ephemeralPinned != nil {
			ephemeralPinned.CloseIdleConnections()
		}
	}
	defer func() {
		lockPooled(nil)
		if session != nil {
			if err := session.Close(); err != nil && retErr == nil {
				if retResp != nil && retResp.Body != nil {
					_ = retResp.Body.Close()
					retResp = nil
				}
				removePooled()
				closeEphemeral()
				retErr = fmt.Errorf("close upstream auth session: %w", err)
			}
		}
		s.recordSuccessfulAuthMechanism(retResp, retErr, authAttempted, mechanism.Result())
	}()

	// Ephemeral pinned transports preserve legacy behavior when a reusable
	// identity cannot be established. Pooled transports are owned by the
	// bounded server cache and deliberately survive response-body close.
	finish := func(r *http.Response) *http.Response {
		if ephemeralPinned != nil && r != nil && r.Body != nil {
			r.Body = &transportClosingBody{ReadCloser: r.Body, transport: ephemeralPinned}
		}
		return r
	}
	failAuth := func(err error) error {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		removePooled()
		closeEphemeral()
		return err
	}

	for attempts := 0; attempts < 3 && resp.StatusCode == http.StatusProxyAuthRequired; attempts++ {
		debug.Dprintf("HTTP proxy auth challenge (attempt %d): %s", attempts+1, targetURL)
		challenge := selectProxyAuthenticateChallenge(effectiveUpstreamAuth(cfg), resp.Header.Values("Proxy-Authenticate"))
		mechanism.ObserveHeader(challenge)
		scheme := authSchemeFromChallenge(challenge)
		if isConnectionAuth(scheme) {
			replayResp, retry, prepareErr := s.prepareUpstreamConnectionAuth(
				req, u, body, candidate, authIdentity, scheme, &transport,
				&activePooled, &ephemeralPinned, lockPooled, removePooled, resp,
			)
			if prepareErr != nil {
				return nil, prepareErr
			}
			if retry {
				if replayResp.StatusCode != http.StatusProxyAuthRequired {
					return replayResp, nil
				}
				resp = replayResp
				activePooled.authenticated = false
				continue
			}
		}

		auth, authErr := s.upstreamProxyAuthorization(cfg, challenge, proxyHost, req, targetURL, passthroughAuth, resp, &mechanism, &session, &usedSSPI)
		if authErr != nil {
			if allowPrompt && usedSSPI && resp.StatusCode == http.StatusProxyAuthRequired && upstreamAuthPromptEnabled() {
				removePooled()
				return s.retryHTTPProxyAuthAfterSSPIFailure(transport, req, u, body, targetURL, passthroughAuth, proxyHost, candidate, resp, &session, closeEphemeral, finish)
			}
			return nil, failAuth(authErr)
		}
		if auth == "" {
			s.forceKerberosReloadForUpstreamAuth(resp)
			removePooled()
			return finish(resp), nil
		}
		mechanism.ObserveHeader(auth)
		authAttempted = true
		if err := drainUpstream407Body(resp.Body); err != nil {
			removePooled()
			closeEphemeral()
			return nil, err
		}
		nextReq, reqErr := s.newOutboundRequest(req, u, body, auth)
		if reqErr != nil {
			removePooled()
			closeEphemeral()
			return nil, reqErr
		}
		nextResp, roundTripErr := transport.RoundTrip(nextReq)
		if roundTripErr != nil {
			debug.Dprint("HTTP proxy auth retry failed: " + roundTripErr.Error())
			removePooled()
			closeEphemeral()
			return nil, roundTripErr
		}
		resp = nextResp
		if activePooled != nil {
			activePooled.authenticated = resp.StatusCode != http.StatusProxyAuthRequired
		}
	}

	s.forceKerberosReloadForUpstreamAuth(resp)
	if resp != nil && resp.StatusCode == http.StatusProxyAuthRequired {
		removePooled()
		if allowPrompt && usedSSPI && (cfg.Username == "" || cfg.Password == "") && upstreamAuthPromptEnabled() {
			return s.retryHTTPProxyAuthAfterSSPIFailure(transport, req, u, body, targetURL, passthroughAuth, proxyHost, candidate, resp, &session, closeEphemeral, finish)
		}
	}
	return finish(resp), nil
}

func (s *Server) skipUpstreamAuthRetry(req *http.Request, body *replayableBody, pooled *transportCacheEntry, pooledAlreadyLocked bool, resp *http.Response) bool {
	if body != nil || req.Body == nil || req.Body == http.NoBody {
		return false
	}
	if pooled != nil {
		if pooledAlreadyLocked {
			pooled.authenticated = false
			pooled.authMu.Unlock()
		} else {
			pooled.authMu.Lock()
			pooled.authenticated = false
			pooled.authMu.Unlock()
		}
		s.removeCachedTransport(pooled.key, pooled)
	}
	s.forceKerberosReloadForUpstreamAuth(resp)
	return true
}

func (s *Server) prepareUpstreamConnectionAuth(
	req *http.Request, u *url.URL, body *replayableBody, candidate wproxy.Server,
	authIdentity, scheme string, transport **http.Transport, activePooled **transportCacheEntry,
	ephemeralPinned **http.Transport, lockPooled func(*transportCacheEntry), removePooled func(),
	resp *http.Response,
) (*http.Response, bool, error) {
	if *activePooled != nil && !strings.EqualFold((*activePooled).scheme, scheme) {
		// Never share an authenticated socket across incompatible connection-auth schemes.
		removePooled()
		lockPooled(nil)
		*activePooled = nil
	}
	if *activePooled == nil {
		if authIdentity != "" && candidate != wproxy.Direct {
			*activePooled, _ = s.connectionAuthTransportForChallenge(candidate, authIdentity, scheme, *transport)
			lockPooled(*activePooled)
			*transport = (*activePooled).transport
			if !(*activePooled).authenticated {
				return nil, false, nil
			}
			if err := drainUpstream407Body(resp.Body); err != nil {
				removePooled()
				return nil, false, err
			}
			replayReq, err := s.newOutboundRequest(req, u, body, "")
			if err != nil {
				return nil, false, err
			}
			replayResp, err := (*transport).RoundTrip(replayReq)
			if err != nil {
				removePooled()
				return nil, false, err
			}
			return replayResp, true, nil
		}
		if *ephemeralPinned == nil {
			*ephemeralPinned = (*transport).Clone()
			(*ephemeralPinned).MaxConnsPerHost = 1
			(*ephemeralPinned).MaxIdleConnsPerHost = 1
			*transport = *ephemeralPinned
		}
		return nil, false, nil
	}
	*transport = (*activePooled).transport
	return nil, false, nil
}

func (s *Server) upstreamProxyAuthorization(
	cfg config.Config, challenge, proxyHost string, req *http.Request, targetURL, passthroughAuth string,
	resp *http.Response, mechanism *authMechanismObservation, session *authSession, usedSSPI *bool,
) (string, error) {
	if *session != nil {
		auth, err := sspiSessionAuth(*session, challenge)
		if err != nil {
			return "", fmt.Errorf("continue upstream proxy authentication: %w", err)
		}
		return auth, nil
	}
	if sspiSessionCandidate(cfg, challenge) {
		*usedSSPI = true
	}
	started, auth, handled, err := s.startUpstreamAuthSession(cfg, challenge, proxyHost, resp, mechanism)
	*session = started
	if err != nil {
		return "", err
	}
	if handled {
		return auth, nil
	}
	return upstreamProxyAuthHeader(cfg, req.Method, targetURL, challenge, passthroughAuth), nil
}

func (s *Server) retryHTTPProxyAuthAfterSSPIFailure(
	transport *http.Transport, req *http.Request, u *url.URL, body *replayableBody, targetURL, passthroughAuth,
	proxyHost string, candidate wproxy.Server, resp *http.Response, session *authSession,
	closeEphemeral func(), finish func(*http.Response) *http.Response,
) (*http.Response, error) {
	if *session != nil {
		err := (*session).Close()
		*session = nil
		if err != nil {
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			closeEphemeral()
			return nil, fmt.Errorf("close failed SSO session before credential fallback: %w", err)
		}
	}
	closeEphemeral()
	return s.retryHTTPProxyAuthWithPrompt(transport, req, u, body, targetURL, passthroughAuth, proxyHost, candidate, resp, finish)
}

func (s *Server) retryHTTPProxyAuthWithPrompt(transport *http.Transport, req *http.Request, u *url.URL, body *replayableBody, targetURL, passthroughAuth, proxyHost string, candidate wproxy.Server, resp *http.Response, finish func(*http.Response) *http.Response) (*http.Response, error) {
	cfg, remember, prompted, releasePrompt, err := s.promptUpstreamCredentials(proxyHost)
	if err != nil {
		debug.Dprint("HTTP proxy credential prompt unavailable: " + err.Error())
		return finish(resp), nil
	}
	if !prompted {
		return finish(resp), nil
	}
	defer releasePrompt()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	// NTLM challenges are connection-bound. Start the password fallback on a
	// fresh transport instead of replaying credentials on the failed SSO socket.
	freshTransport := transport.Clone()
	freshTransport.CloseIdleConnections()
	defer freshTransport.CloseIdleConnections()
	firstReq, err := s.newOutboundRequest(req, u, body, "")
	if err != nil {
		return nil, err
	}
	firstResp, err := freshTransport.RoundTrip(firstReq)
	if err != nil {
		return nil, fmt.Errorf("retry upstream proxy with prompted credentials: %w", err)
	}
	if firstResp.StatusCode != http.StatusProxyAuthRequired {
		if firstResp.Body != nil {
			firstResp.Body = &transportClosingBody{ReadCloser: firstResp.Body, transport: freshTransport}
		}
		return firstResp, nil
	}

	identity := upstreamConnectionAuthIdentity(cfg, "")
	result, err := s.retryHTTPProxyAuthWithPoolConfig(freshTransport, req, u, body, targetURL, passthroughAuth, proxyHost, candidate, identity, nil, false, firstResp, cfg, false)
	if err != nil {
		return nil, err
	}
	if identity == "" && result != nil && result.Body != nil {
		result.Body = &transportClosingBody{ReadCloser: result.Body, transport: freshTransport}
	}
	if result != nil && result.StatusCode != http.StatusProxyAuthRequired {
		s.acceptPromptedCredentials(cfg, remember)
	}
	return result, nil
}

func (s *Server) startUpstreamAuthSession(cfg config.Config, challenge, proxyHost string, resp *http.Response, mechanism *authMechanismObservation) (authSession, string, bool, error) {
	if kerberosSessionCandidate(cfg, challenge) {
		mechanism.ObserveMechanism(authMechanismKerberos)
		session, err := kerberosSessionFactory(s.krb, proxyHost)
		if err != nil {
			s.forceKerberosReloadForUpstreamAuth(resp)
			return nil, "", true, fmt.Errorf("start Kerberos SPNEGO proxy authentication: %w", err)
		}
		auth, err := session.Negotiate()
		if err != nil {
			return session, "", true, fmt.Errorf("start Kerberos SPNEGO negotiation: %w", err)
		}
		return session, auth, true, nil
	}
	if sspiSessionCandidate(cfg, challenge) {
		session, err := sspiSessionFactory(challenge, proxyHost)
		if err != nil {
			return nil, "", true, fmt.Errorf("start SSPI proxy authentication: %w", err)
		}
		auth, err := session.Negotiate()
		if err != nil {
			return session, "", true, fmt.Errorf("start SSPI proxy negotiation: %w", err)
		}
		return session, auth, true, nil
	}
	return nil, "", false, nil
}

const maxUpstream407Body = 64 << 10

func drainUpstream407Body(body io.ReadCloser) error {
	if body == nil {
		return nil
	}
	defer body.Close()

	n, err := io.CopyN(io.Discard, body, maxUpstream407Body+1)
	switch {
	case err == nil && n > maxUpstream407Body:
		return fmt.Errorf("upstream 407 body exceeds %d bytes", maxUpstream407Body)
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return fmt.Errorf("drain upstream 407 body: %w", err)
	default:
		return nil
	}
}

// transportClosingBody releases a single-connection pinned transport after the
// response body has been consumed and closed.
type transportClosingBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b *transportClosingBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return err
}

// sspiSessionAuth picks Negotiate or Authenticate depending on whether the
// server challenge header contains a token.
func sspiSessionAuth(session authSession, challenge string) (string, error) {
	_, tokenPart, hasToken := strings.Cut(strings.TrimSpace(challenge), " ")
	if hasToken && strings.TrimSpace(tokenPart) != "" {
		return session.Authenticate(challenge)
	}
	return session.Negotiate()
}

func (s *Server) forceKerberosReloadForUpstreamAuth(resp *http.Response) {
	if s.krb == nil || resp == nil || resp.StatusCode != http.StatusProxyAuthRequired {
		return
	}
	if findProxyAuthenticateChallenge(resp.Header.Values("Proxy-Authenticate"), authNegotiate) == "" {
		return
	}
	s.reloadKerberos(true)
}

func upstreamProxyAuthHeader(cfg config.Config, method, uri, challenge, passthroughAuth string) string {
	authModes := upstreamAuthModes(effectiveUpstreamAuth(cfg))
	if len(authModes) == 0 {
		return passthroughAuth
	}
	if cfg.Username == "" || cfg.Password == "" {
		return ""
	}
	challengeScheme := authSchemeFromChallenge(challenge)
	if challengeScheme == "" || !containsAuthMode(authModes, challengeScheme) {
		return ""
	}
	authMode := challengeScheme
	if authMode == authDigest {
		scheme, paramsText, ok := strings.Cut(strings.TrimSpace(challenge), " ")
		if !ok || !strings.EqualFold(scheme, authSchemeDigest) || strings.TrimSpace(paramsText) == "" {
			return ""
		}
		params := parseAuthParams(strings.TrimSpace(paramsText))
		realm := params["realm"]
		nonce := params["nonce"]
		qop, qopOK := selectDigestQop(params["qop"])
		algorithm := strings.TrimSpace(params["algorithm"])
		if realm == "" || nonce == "" || !qopOK || (algorithm != "" && !strings.EqualFold(algorithm, "MD5")) {
			return ""
		}
		ha1 := md5hex(cfg.Username + ":" + realm + ":" + cfg.Password)
		ha2 := md5hex(method + ":" + uri)
		if qop == "" {
			response := md5hex(ha1 + ":" + nonce + ":" + ha2)
			return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
				cfg.Username, realm, nonce, uri, response)
		}
		nc := nextDigestNC(nonce)
		cnonce := newCnonce()
		response := md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
		return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=%s, nc=%s, cnonce="%s", response="%s"`,
			cfg.Username, realm, nonce, uri, qop, nc, cnonce, response)
	}
	if isConnectionAuth(authMode) {
		auth, _ := connectionProxyAuthHeader(cfg, authMode, challenge)
		return auth
	}
	return authSchemeBasic + " " + base64.StdEncoding.EncodeToString([]byte(cfg.Username+":"+cfg.Password))
}

func selectDigestQop(qop string) (string, bool) {
	qop = strings.TrimSpace(qop)
	if qop == "" {
		return "", true
	}
	for _, part := range strings.Split(qop, ",") {
		if strings.EqualFold(strings.TrimSpace(part), digestQopAuth) {
			return digestQopAuth, true
		}
	}
	return "", false
}

func effectiveUpstreamAuth(cfg config.Config) string {
	if strings.TrimSpace(cfg.Auth) == "" && cfg.Username != "" && cfg.Password != "" {
		return authAnySafe
	}
	return cfg.Auth
}

func hasConnectionAuthMode(auth string) bool {
	for _, mode := range upstreamAuthModes(auth) {
		if isConnectionAuth(mode) {
			return true
		}
	}
	return false
}

var upstreamAuthIdentityKey = func() [32]byte {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		panic(fmt.Sprintf("generate upstream auth identity key: %v", err))
	}
	return key
}()

func upstreamConnectionAuthIdentity(cfg config.Config, _ string) string {
	if !hasConnectionAuthMode(effectiveUpstreamAuth(cfg)) {
		return ""
	}
	if cfg.Username != "" && cfg.Password != "" {
		authMode := strings.ToUpper(strings.TrimSpace(effectiveUpstreamAuth(cfg)))
		mac := hmac.New(sha256.New, upstreamAuthIdentityKey[:])
		_, _ = mac.Write([]byte("explicit\x00" + cfg.Username + "\x00" + cfg.Password + "\x00" + authMode))
		return "explicit:" + hex.EncodeToString(mac.Sum(nil))
	}
	if cfg.Username != "" || cfg.Password != "" {
		return ""
	}
	if runtime.GOOS == goosWindows {
		return "sspi:current-user"
	}
	// A downstream Proxy-Authorization token is not an authoritative reusable
	// identity for a connection-oriented upstream pool. Keep passthrough auth
	// ephemeral so two downstream sessions can never share authenticated state.
	return ""
}

func UpstreamProxyAuthHeader(cfg config.Config, method, uri string, challenges []string) string {
	challenge := ""
	if len(challenges) != 0 {
		challenge = selectProxyAuthenticateChallenge(effectiveUpstreamAuth(cfg), challenges)
	}
	return upstreamProxyAuthHeader(cfg, method, uri, challenge, "")
}

func upstreamAuthModes(auth string) []string {
	auth = strings.ToUpper(strings.TrimSpace(auth))
	if auth == "" || auth == authAny {
		return []string{authNegotiate, authNTLM, authDigest, authBasic}
	}
	if auth == authNone {
		return nil
	}
	if auth == authAnySafe {
		return []string{authNegotiate, authNTLM, authDigest}
	}
	for _, prefix := range []struct {
		name string
		base []string
		only bool
	}{
		{"SAFENO", []string{authNegotiate, authNTLM, authDigest}, false},
		{"ONLY", []string{authNegotiate, authNTLM, authDigest, authBasic}, true},
		{"NO", []string{authNegotiate, authNTLM, authDigest, authBasic}, false},
	} {
		if method, ok := strings.CutPrefix(auth, prefix.name); ok && isKnownAuthScheme(method) {
			if prefix.only {
				return []string{method}
			}
			return removeAuthMode(prefix.base, method)
		}
	}
	return []string{auth}
}

func validateUpstreamAuth(auth string) error {
	auth = strings.ToUpper(strings.TrimSpace(auth))
	if auth == "" || auth == authAny || auth == authAnySafe || auth == authNone {
		return nil
	}
	for _, prefix := range []string{"SAFENO", "ONLY", "NO"} {
		if method, ok := strings.CutPrefix(auth, prefix); ok && isKnownAuthScheme(method) {
			return nil
		}
	}
	if isKnownAuthScheme(auth) {
		return nil
	}
	return fmt.Errorf("unsupported upstream auth type: %s", auth)
}

func selectProxyAuthenticateChallenge(auth string, challenges []string) string {
	for _, scheme := range upstreamAuthModes(auth) {
		if challenge := findProxyAuthenticateChallenge(challenges, scheme); challenge != "" {
			return challenge
		}
	}
	return ""
}

func removeAuthMode(modes []string, remove string) []string {
	out := make([]string, 0, len(modes))
	for _, mode := range modes {
		if mode != remove {
			out = append(out, mode)
		}
	}
	return out
}

func containsAuthMode(modes []string, want string) bool {
	for _, mode := range modes {
		if strings.EqualFold(mode, want) {
			return true
		}
	}
	return false
}

func findProxyAuthenticateChallenge(challenges []string, scheme string) string {
	for _, challenge := range challenges {
		for _, part := range splitProxyAuthenticateValues(challenge) {
			if strings.EqualFold(authSchemeFromChallenge(part), scheme) {
				return strings.TrimSpace(part)
			}
		}
	}
	return ""
}

func splitProxyAuthenticateValues(header string) []string {
	var raw []string
	start := 0
	inQuote := false
	for i, r := range header {
		switch r {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				raw = append(raw, header[start:i])
				start = i + 1
			}
		}
	}
	raw = append(raw, header[start:])
	var parts []string
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if isKnownAuthScheme(authSchemeFromChallenge(part)) || len(parts) == 0 {
			parts = append(parts, part)
			continue
		}
		parts[len(parts)-1] += ", " + part
	}
	return parts
}

func authSchemeFromChallenge(challenge string) string {
	scheme, _, _ := strings.Cut(strings.TrimSpace(challenge), " ")
	return strings.ToUpper(scheme)
}

func isKnownAuthScheme(scheme string) bool {
	switch strings.ToUpper(scheme) {
	case authNegotiate, authNTLM, authDigest, authBasic:
		return true
	default:
		return false
	}
}

func isConnectionAuth(auth string) bool {
	return strings.EqualFold(auth, authNTLM) || strings.EqualFold(auth, authNegotiate)
}

func connectionProxyAuthHeader(cfg config.Config, authMode, challengeHeader string) (string, error) {
	if cfg.Username == "" || cfg.Password == "" {
		return "", nil
	}
	scheme, token, _ := strings.Cut(strings.TrimSpace(challengeHeader), " ")
	if !strings.EqualFold(scheme, authMode) {
		return "", nil
	}
	if strings.TrimSpace(token) == "" {
		msg, err := ntlmssp.NewNegotiateMessage("", "")
		if err != nil {
			return "", err
		}
		if strings.EqualFold(authMode, authNegotiate) {
			msg = spnegoNegTokenInit(msg)
		}
		return canonicalConnectionAuthScheme(scheme) + " " + base64.StdEncoding.EncodeToString(msg), nil
	}
	challenge, err := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return "", err
	}
	wrapResponse := false
	if strings.EqualFold(authMode, authNegotiate) && !isNTLMSSP(challenge) {
		unwrapped, ok := unwrapSPNEGONTLMToken(challenge)
		if !ok {
			return "", nil
		}
		challenge = unwrapped
		wrapResponse = true
	}
	msg, err := ntlmssp.NewAuthenticateMessage(challenge, cfg.Username, cfg.Password, nil)
	if err != nil {
		return "", err
	}
	if wrapResponse {
		msg = spnegoNegTokenResp(msg)
	}
	return canonicalConnectionAuthScheme(scheme) + " " + base64.StdEncoding.EncodeToString(msg), nil
}

func canonicalConnectionAuthScheme(scheme string) string {
	if strings.EqualFold(scheme, authNTLM) {
		return authNTLM
	}
	return authSchemeNeg
}
