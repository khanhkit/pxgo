package proxy

import (
	"errors"
	"testing"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestPromptUpstreamCredentialsKeepsAcceptedCredentialsInRuntime(t *testing.T) {
	oldPrompt := upstreamCredentialPrompt
	t.Cleanup(func() { upstreamCredentialPrompt = oldPrompt })
	upstreamCredentialPrompt = func(proxyHost string) (promptedUpstreamCredential, error) {
		if proxyHost != "proxy.corp.example" {
			t.Fatalf("proxy host=%q", proxyHost)
		}
		return promptedUpstreamCredential{Username: "DOMAIN\\user", Password: "secret"}, nil
	}

	s := &Server{cfg: config.Default()}
	cfg, remember, prompted, release, err := s.promptUpstreamCredentials("proxy.corp.example")
	if err != nil {
		t.Fatal(err)
	}
	if !prompted || remember {
		t.Fatalf("prompted=%t remember=%t, want true/false", prompted, remember)
	}
	if got := s.upstreamAuthConfig(); got.Username != "" || got.Password != "" {
		t.Fatalf("unverified credentials became active before acceptance: %#v", got)
	}
	s.acceptPromptedCredentials(cfg, remember)
	release()
	if got := s.upstreamAuthConfig(); got.Username != "DOMAIN\\user" || got.Password != "secret" {
		t.Fatalf("runtime credentials=%q/%q", got.Username, got.Password)
	}
}

func TestPromptUpstreamCredentialsCancelReleasesPromptLock(t *testing.T) {
	oldPrompt := upstreamCredentialPrompt
	t.Cleanup(func() { upstreamCredentialPrompt = oldPrompt })
	upstreamCredentialPrompt = func(string) (promptedUpstreamCredential, error) {
		return promptedUpstreamCredential{}, errUpstreamCredentialPromptCancelled
	}

	s := &Server{cfg: config.Default()}
	_, _, prompted, release, err := s.promptUpstreamCredentials("proxy.corp.example")
	if err != nil || prompted || release != nil {
		t.Fatalf("prompted=%t release-present=%t err=%v, want clean cancellation", prompted, release != nil, err)
	}

	upstreamCredentialPrompt = func(string) (promptedUpstreamCredential, error) {
		return promptedUpstreamCredential{}, errors.New("dialog unavailable")
	}
	_, _, _, _, err = s.promptUpstreamCredentials("proxy.corp.example")
	if err == nil || err.Error() != "dialog unavailable" {
		t.Fatalf("second prompt error=%v, want dialog error after lock release", err)
	}
}

func TestUpstreamAuthPromptCanBeDisabledForUnattendedRuns(t *testing.T) {
	t.Setenv("PXGO_AUTH_PROMPT", "0")
	if upstreamAuthPromptEnabled() {
		t.Fatal("auth prompt remained enabled with PXGO_AUTH_PROMPT=0")
	}
	t.Setenv("PXGO_AUTH_PROMPT", "1")
	if !upstreamAuthPromptEnabled() {
		t.Fatal("auth prompt did not re-enable")
	}
}
