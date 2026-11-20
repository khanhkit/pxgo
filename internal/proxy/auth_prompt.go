package proxy

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/debug"
)

const upstreamCredentialAccount = "credentials"

var errUpstreamCredentialPromptCancelled = errors.New("upstream credential prompt cancelled")

type upstreamCredential struct {
	username string
	password string
	prompted bool
}

type promptedUpstreamCredential struct {
	Username string
	Password string
	Remember bool
}

type pendingUpstreamPrompt struct {
	credential           promptedUpstreamCredential
	retryDialogOnFailure bool
	users                int
}

type storedUpstreamCredential struct {
	Username string `json:"username"`
	Value    string `json:"value"`
}

var upstreamCredentialPrompt = promptWindowsUpstreamCredentials

func upstreamAuthPromptEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("PXGO_AUTH_PROMPT")), "0")
}

func normalizeUpstreamProxyHost(host string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(host)), ".")
}

func upstreamCredentialRealm(proxyHost string) string {
	return config.Realm + ":" + normalizeUpstreamProxyHost(proxyHost)
}

func (s *Server) upstreamAuthConfig(proxyHost string) config.Config {
	cfg := s.cfg
	if cfg.Username != "" || cfg.Password != "" {
		return cfg
	}
	host := normalizeUpstreamProxyHost(proxyHost)
	if host == "" {
		return cfg
	}
	s.authStateMu.Lock()
	credential, ok := s.upstreamCredentials[host]
	s.authStateMu.Unlock()
	if ok {
		cfg.Username = credential.username
		cfg.Password = credential.password
	}
	return cfg
}

// promptUpstreamCredentials serializes only the native dialog. The pending
// entry remains visible while callers retry, allowing concurrent 407s for the
// same proxy to reuse one prompt without holding the mutex across network I/O.
func (s *Server) promptUpstreamCredentials(proxyHost string, forceDialog bool) (config.Config, bool, bool, bool, func(), error) {
	host := normalizeUpstreamProxyHost(proxyHost)
	cfg := s.cfg
	if host == "" {
		return cfg, false, false, false, nil, errors.New("upstream proxy host is empty")
	}
	if cfg.Username != "" || cfg.Password != "" {
		return cfg, false, true, false, func() {}, nil
	}

	var saved promptedUpstreamCredential
	savedOK := false
	if !forceDialog {
		if encoded, ok := config.GetPassword(upstreamCredentialRealm(host), upstreamCredentialAccount); ok {
			var stored storedUpstreamCredential
			if err := json.Unmarshal([]byte(encoded), &stored); err == nil && stored.Username != "" && stored.Value != "" {
				saved = promptedUpstreamCredential{Username: stored.Username, Password: stored.Value}
				savedOK = true
			} else {
				debug.Dprint("ignore invalid host-scoped upstream credential entry")
			}
		}
	}

	// Serialize only the native dialog. Keep runtime credential reads independent
	// so ordinary requests to this or other proxies do not stall while it is open.
	s.authPromptMu.Lock()
	defer s.authPromptMu.Unlock()

	s.authStateMu.Lock()
	if s.upstreamCredentials != nil {
		if credential, ok := s.upstreamCredentials[host]; ok && !forceDialog {
			cfg.Username = credential.username
			cfg.Password = credential.password
			s.authStateMu.Unlock()
			return cfg, false, true, true, func() {}, nil
		}
		if forceDialog {
			delete(s.upstreamCredentials, host)
		}
	}
	if s.pendingAuthPrompts != nil {
		if pending := s.pendingAuthPrompts[host]; pending != nil && (!forceDialog || !pending.retryDialogOnFailure) {
			pending.users++
			cfg.Username = pending.credential.Username
			cfg.Password = pending.credential.Password
			release := s.releaseUpstreamPrompt(host, pending)
			retryDialog := pending.retryDialogOnFailure
			remember := pending.credential.Remember
			s.authStateMu.Unlock()
			return cfg, remember, true, retryDialog, release, nil
		}
	}
	s.authStateMu.Unlock()
	if !upstreamAuthPromptEnabled() {
		return cfg, false, false, false, nil, nil
	}

	credential := saved
	fromSaved := savedOK && !forceDialog
	if !fromSaved {
		var err error
		credential, err = upstreamCredentialPrompt(host)
		if err != nil {
			if errors.Is(err, errUpstreamCredentialPromptCancelled) {
				return cfg, false, false, false, nil, nil
			}
			return cfg, false, false, false, nil, err
		}
	}
	if strings.TrimSpace(credential.Username) == "" || credential.Password == "" {
		return cfg, false, false, false, nil, errors.New("username and password are required")
	}
	s.authStateMu.Lock()
	if s.pendingAuthPrompts == nil {
		s.pendingAuthPrompts = make(map[string]*pendingUpstreamPrompt)
	}
	pending := &pendingUpstreamPrompt{
		credential:           credential,
		retryDialogOnFailure: fromSaved,
		users:                1,
	}
	s.pendingAuthPrompts[host] = pending
	s.authStateMu.Unlock()
	cfg.Username = credential.Username
	cfg.Password = credential.Password
	release := s.releaseUpstreamPrompt(host, pending)
	return cfg, credential.Remember, true, fromSaved, release, nil
}

func (s *Server) releaseUpstreamPrompt(host string, pending *pendingUpstreamPrompt) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.authStateMu.Lock()
			pending.users--
			if pending.users <= 0 && s.pendingAuthPrompts[host] == pending {
				delete(s.pendingAuthPrompts, host)
			}
			s.authStateMu.Unlock()
		})
	}
}

func (s *Server) acceptPromptedCredentials(proxyHost string, cfg config.Config, remember bool) {
	host := normalizeUpstreamProxyHost(proxyHost)
	s.authStateMu.Lock()
	if s.upstreamCredentials == nil {
		s.upstreamCredentials = make(map[string]upstreamCredential)
	}
	s.upstreamCredentials[host] = upstreamCredential{username: cfg.Username, password: cfg.Password, prompted: true}
	s.authStateMu.Unlock()

	if remember {
		encoded, err := json.Marshal(storedUpstreamCredential{Username: cfg.Username, Value: cfg.Password})
		if err == nil {
			if err := config.StorePassword(upstreamCredentialRealm(host), upstreamCredentialAccount, string(encoded)); err != nil {
				// Authentication succeeded; keyring failure must not fail the request.
				debug.Dprint("save accepted host-scoped upstream credentials: " + err.Error())
			}
		}
	}
}

func (s *Server) hasPromptedCredentials(proxyHost string, cfg config.Config) bool {
	host := normalizeUpstreamProxyHost(proxyHost)
	s.authStateMu.Lock()
	credential, ok := s.upstreamCredentials[host]
	s.authStateMu.Unlock()
	return ok && credential.prompted && credential.username == cfg.Username && credential.password == cfg.Password
}
