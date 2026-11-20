package proxy

import (
	"errors"
	"os"
	"strings"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/debug"
)

var errUpstreamCredentialPromptCancelled = errors.New("upstream credential prompt cancelled")

type upstreamCredential struct {
	username string
	password string
}

type promptedUpstreamCredential struct {
	Username string
	Password string
	Remember bool
}

var upstreamCredentialPrompt = promptWindowsUpstreamCredentials

func upstreamAuthPromptEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("PXGO_AUTH_PROMPT")), "0")
}

func (s *Server) upstreamAuthConfig() config.Config {
	cfg := s.cfg
	if credentials := s.upstreamCredentials.Load(); credentials != nil {
		cfg.Username = credentials.username
		cfg.Password = credentials.password
	}
	return cfg
}

// promptUpstreamCredentials keeps the prompt lock until the caller has tested
// the credentials, preventing concurrent 407 responses from opening duplicate dialogs.
func (s *Server) promptUpstreamCredentials(proxyHost string) (config.Config, bool, bool, func(), error) {
	s.authPromptMu.Lock()
	cfg := s.upstreamAuthConfig()
	if cfg.Username != "" && cfg.Password != "" {
		s.authPromptMu.Unlock()
		return cfg, false, true, func() {}, nil
	}
	if !upstreamAuthPromptEnabled() {
		s.authPromptMu.Unlock()
		return cfg, false, false, nil, nil
	}

	credentials, err := upstreamCredentialPrompt(proxyHost)
	if err != nil {
		s.authPromptMu.Unlock()
		if errors.Is(err, errUpstreamCredentialPromptCancelled) {
			return cfg, false, false, nil, nil
		}
		return cfg, false, false, nil, err
	}
	if strings.TrimSpace(credentials.Username) == "" || credentials.Password == "" {
		s.authPromptMu.Unlock()
		return cfg, false, false, nil, errors.New("username and password are required")
	}
	cfg.Username = credentials.Username
	cfg.Password = credentials.Password
	return cfg, credentials.Remember, true, s.authPromptMu.Unlock, nil
}

func (s *Server) acceptPromptedCredentials(cfg config.Config, remember bool) {
	s.upstreamCredentials.Store(&upstreamCredential{username: cfg.Username, password: cfg.Password})
	if remember {
		if err := config.StorePassword(config.Realm, cfg.Username, cfg.Password); err != nil {
			// A keyring failure must not turn a successful authenticated request into failure.
			debug.Dprint("save accepted upstream credentials: " + err.Error())
		}
	}
}
