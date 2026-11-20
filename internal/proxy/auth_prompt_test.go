package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/wproxy"
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

func TestNormalizeUpstreamProxyHostRemovesAllTrailingDots(t *testing.T) {
	for input, want := range map[string]string{
		" Proxy.Example. ":   "proxy.example",
		"PROXY.EXAMPLE..":    "proxy.example",
		" proxy.example... ": "proxy.example",
	} {
		if got := normalizeUpstreamProxyHost(input); got != want {
			t.Errorf("normalizeUpstreamProxyHost(%q)=%q, want %q", input, got, want)
		}
	}
}

type trackedPromptResponseBody struct {
	*strings.Reader
	closed bool
}

func (b *trackedPromptResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestDetachUpstream407BodyPreservesBodyAndClosesResponse(t *testing.T) {
	body := &trackedPromptResponseBody{Reader: strings.NewReader("proxy credentials required")}
	resp := &http.Response{
		StatusCode:    http.StatusProxyAuthRequired,
		Body:          body,
		ContentLength: -1,
		Header:        make(http.Header),
	}
	resp.Header.Set("Transfer-Encoding", "chunked")

	if err := detachUpstream407Body(resp); err != nil {
		t.Fatal(err)
	}
	if !body.closed {
		t.Fatal("upstream 407 body was not closed before showing the credential dialog")
	}
	if resp.ContentLength != int64(len("proxy credentials required")) {
		t.Fatalf("content length=%d", resp.ContentLength)
	}
	if resp.Header.Get("Transfer-Encoding") != "" {
		t.Fatal("stale transfer-encoding header remained after buffering")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "proxy credentials required" {
		t.Fatalf("detached body=%q", got)
	}
	_ = resp.Body.Close()
}

func TestDetachUpstream407BodyRejectsOversizedBody(t *testing.T) {
	body := &trackedPromptResponseBody{Reader: strings.NewReader(strings.Repeat("x", maxUpstream407Body+1))}
	resp := &http.Response{Body: body}
	if err := detachUpstream407Body(resp); err == nil {
		t.Fatal("expected oversized 407 body to be rejected")
	}
	if !body.closed {
		t.Fatal("oversized upstream 407 body was not closed")
	}
}

type failingPromptResponseBody struct {
	readErr error
	closed  bool
}

func (b *failingPromptResponseBody) Read([]byte) (int, error) {
	return 0, b.readErr
}

func (b *failingPromptResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestRetryHTTPProxyAuthWithPromptPreserves407WhenBodyDetachFails(t *testing.T) {
	upstreamBody := &failingPromptResponseBody{readErr: errors.New("upstream 407 body read failed")}
	resp := &http.Response{
		StatusCode: http.StatusProxyAuthRequired,
		Body:       upstreamBody,
		Header:     make(http.Header),
	}
	s := &Server{cfg: config.Default()}
	got, err := s.retryHTTPProxyAuthWithPrompt(nil, nil, nil, nil, "", "", "proxy.corp.example", wproxy.Server{}, resp, func(r *http.Response) *http.Response { return r })
	if err != nil {
		t.Fatalf("prompt fallback returned error instead of 407: %v", err)
	}
	if got != resp || got.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("response=%v, want original 407", got)
	}
	if !upstreamBody.closed {
		t.Fatal("upstream response body was not closed")
	}
	if got.Body != http.NoBody {
		t.Fatal("failed detach should return an empty, closed 407 body")
	}
}

func TestRetryConnectWithPromptSkipsUnsupportedProxyScheme(t *testing.T) {
	oldPrompt := upstreamCredentialPrompt
	t.Cleanup(func() { upstreamCredentialPrompt = oldPrompt })
	called := false
	upstreamCredentialPrompt = func(string) (promptedUpstreamCredential, error) {
		called = true
		return promptedUpstreamCredential{Username: "user", Password: "secret"}, nil
	}

	s := &Server{cfg: config.Default()}
	conn, leftover, err := s.retryConnectWithPrompt(
		context.Background(),
		"example.com:443",
		"",
		wproxy.Server{Scheme: "socks5", Host: "proxy.corp.example", Port: 1080},
		func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("unsupported proxy scheme must not dial")
			return nil, nil
		},
		time.Second,
	)
	if err != nil || conn != nil || leftover != nil {
		t.Fatalf("result=(%v,%v,%v), want (nil,nil,nil)", conn, leftover, err)
	}
	if called {
		t.Fatal("unsupported proxy scheme must not open credential dialog")
	}
}
