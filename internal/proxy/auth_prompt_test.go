package proxy

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestPromptUpstreamCredentialsKeepsAcceptedCredentialsScopedToProxy(t *testing.T) {
	oldPrompt := upstreamCredentialPrompt
	t.Cleanup(func() { upstreamCredentialPrompt = oldPrompt })
	upstreamCredentialPrompt = func(proxyHost string) (promptedUpstreamCredential, error) {
		if proxyHost != "proxy-a.corp.example" {
			t.Fatalf("proxy host=%q", proxyHost)
		}
		return promptedUpstreamCredential{Username: "DOMAIN\\user", Password: "secret"}, nil
	}

	s := &Server{cfg: config.Default()}
	cfg, remember, prompted, _, release, err := s.promptUpstreamCredentials("proxy-a.corp.example", false)
	if err != nil {
		t.Fatal(err)
	}
	if !prompted || remember {
		t.Fatalf("prompted=%t remember=%t, want true/false", prompted, remember)
	}
	if got := s.upstreamAuthConfig("proxy-a.corp.example"); got.Username != "" || got.Password != "" {
		t.Fatalf("unverified credentials became active before acceptance: %#v", got)
	}
	s.acceptPromptedCredentials("proxy-a.corp.example", cfg, remember)
	release()
	if got := s.upstreamAuthConfig("proxy-a.corp.example"); got.Username != "DOMAIN\\user" || got.Password != "secret" {
		t.Fatalf("proxy A credentials=%q/%q", got.Username, got.Password)
	}
	if got := s.upstreamAuthConfig("proxy-b.corp.example"); got.Username != "" || got.Password != "" {
		t.Fatalf("proxy A credentials leaked to proxy B: %q/%q", got.Username, got.Password)
	}
}

func TestPromptUpstreamCredentialsCoalescesConcurrentRequestsByProxy(t *testing.T) {
	oldPrompt := upstreamCredentialPrompt
	t.Cleanup(func() { upstreamCredentialPrompt = oldPrompt })
	var calls atomic.Int32
	upstreamCredentialPrompt = func(string) (promptedUpstreamCredential, error) {
		calls.Add(1)
		return promptedUpstreamCredential{Username: "DOMAIN\\user", Password: "secret"}, nil
	}

	s := &Server{cfg: config.Default()}
	_, _, firstPrompted, _, releaseFirst, err := s.promptUpstreamCredentials("proxy.corp.example", false)
	if err != nil || !firstPrompted {
		t.Fatalf("first prompt=%t err=%v", firstPrompted, err)
	}
	_, _, secondPrompted, _, releaseSecond, err := s.promptUpstreamCredentials("PROXY.corp.example", false)
	if err != nil || !secondPrompted {
		t.Fatalf("second prompt=%t err=%v", secondPrompted, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("dialog count=%d, want one", calls.Load())
	}
	releaseFirst()
	releaseSecond()
}

func TestPromptUpstreamCredentialsCancelReleasesPromptLock(t *testing.T) {
	oldPrompt := upstreamCredentialPrompt
	t.Cleanup(func() { upstreamCredentialPrompt = oldPrompt })
	upstreamCredentialPrompt = func(string) (promptedUpstreamCredential, error) {
		return promptedUpstreamCredential{}, errUpstreamCredentialPromptCancelled
	}

	s := &Server{cfg: config.Default()}
	_, _, prompted, _, release, err := s.promptUpstreamCredentials("proxy.corp.example", false)
	if err != nil || prompted || release != nil {
		t.Fatalf("prompted=%t release-present=%t err=%v, want clean cancellation", prompted, release != nil, err)
	}

	upstreamCredentialPrompt = func(string) (promptedUpstreamCredential, error) {
		return promptedUpstreamCredential{}, errors.New("dialog unavailable")
	}
	_, _, _, _, _, err = s.promptUpstreamCredentials("proxy.corp.example", false)
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
