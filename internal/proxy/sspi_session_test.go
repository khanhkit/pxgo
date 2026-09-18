package proxy

import (
	"bufio"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/pavelsimo/pxgo/internal/config"
)

type fakeSSPICredential struct {
	releases int
	err      error
}

func (c *fakeSSPICredential) Release() error {
	c.releases++
	return c.err
}

type fakeSSPIContext struct {
	updates  int
	releases int
	complete bool
	output   []byte
	err      error
}

func (c *fakeSSPIContext) Update(_ []byte) (bool, []byte, error) {
	c.updates++
	return c.complete, c.output, c.err
}

func (c *fakeSSPIContext) Release() error {
	c.releases++
	return nil
}

type fakeAuthSession struct {
	negotiateHeader string
	negotiateErr    error
	authHeader      string
	authErr         error
	closeCount      int
	lastChallenge   string
}

func (s *fakeAuthSession) Negotiate() (string, error) {
	return s.negotiateHeader, s.negotiateErr
}

func (s *fakeAuthSession) Authenticate(challenge string) (string, error) {
	s.lastChallenge = challenge
	return s.authHeader, s.authErr
}

func (s *fakeAuthSession) Close() error {
	s.closeCount++
	return nil
}

// TC-SSPI-REG-001
func TestAPISS0002ManagedSSPISessionPreservesSchemeAndReleasesOnce(t *testing.T) {
	cred := &fakeSSPICredential{}
	ctx := &fakeSSPIContext{complete: true, output: []byte("final")}
	session := newManagedSSPISession("NTLM", []byte("initial"), cred, ctx)

	initial, err := session.Negotiate()
	if err != nil {
		t.Fatal(err)
	}
	if want := "NTLM " + base64.StdEncoding.EncodeToString([]byte("initial")); initial != want {
		t.Fatalf("initial header=%q want=%q", initial, want)
	}

	challengeToken := base64.StdEncoding.EncodeToString([]byte("challenge"))
	final, err := session.Authenticate("NTLM " + challengeToken)
	if err != nil {
		t.Fatal(err)
	}
	if want := "NTLM " + base64.StdEncoding.EncodeToString([]byte("final")); final != want {
		t.Fatalf("final header=%q want=%q", final, want)
	}
	if ctx.releases != 0 || cred.releases != 0 {
		t.Fatalf("resources released before session close: ctx=%d cred=%d", ctx.releases, cred.releases)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if ctx.releases != 1 || cred.releases != 1 {
		t.Fatalf("resource release counts ctx=%d cred=%d, want 1/1", ctx.releases, cred.releases)
	}
}

// TC-SSPI-NEG-002
func TestAPISS0002ManagedSSPISessionRejectsMalformedOrWrongSchemeChallenge(t *testing.T) {
	tests := []string{
		"NTLM",
		"Negotiate " + base64.StdEncoding.EncodeToString([]byte("challenge")),
		"NTLM not-base64%%%",
	}
	for _, challenge := range tests {
		t.Run(challenge, func(t *testing.T) {
			session := newManagedSSPISession("NTLM", []byte("initial"), &fakeSSPICredential{}, &fakeSSPIContext{complete: true, output: []byte("final")})
			if _, err := session.Negotiate(); err != nil {
				t.Fatal(err)
			}
			if _, err := session.Authenticate(challenge); err == nil {
				t.Fatalf("challenge %q unexpectedly accepted", challenge)
			}
		})
	}
}

// TC-SSPI-REG-003
func TestAPISS0002ProxySPNUsesUpstreamProxyHost(t *testing.T) {
	if got, want := proxySPN("proxy.corp.example"), "HTTP/proxy.corp.example"; got != want {
		t.Fatalf("proxySPN=%q want=%q", got, want)
	}
}

// TC-SSPI-REG-004
func TestAPISS0002ConnectPropagatesSessionAuthError(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	session := &fakeAuthSession{negotiateErr: errors.New("synthetic SSPI failure")}
	err := sendUpstreamConnectAttempt(client, bufio.NewReader(client), "example.com:443", "proxy.corp.example", config.Default(), "NTLM", "", 1, session, nil)
	if err == nil || !strings.Contains(err.Error(), "synthetic SSPI failure") {
		t.Fatalf("err=%v, want propagated SSPI failure", err)
	}
}

// TC-SSPI-REG-005
func TestAPISS0002HTTPSSPIStartFailureIsExplicitAndCloses407(t *testing.T) {
	oldCandidate := sspiSessionCandidate
	oldFactory := sspiSessionFactory
	t.Cleanup(func() {
		sspiSessionCandidate = oldCandidate
		sspiSessionFactory = oldFactory
	})
	sspiSessionCandidate = func(config.Config, string) bool { return true }
	sspiSessionFactory = func(challenge, proxyHost string) (authSession, error) {
		if challenge != "NTLM" {
			t.Fatalf("challenge=%q want NTLM", challenge)
		}
		if proxyHost != "proxy.corp.example" {
			t.Fatalf("proxyHost=%q", proxyHost)
		}
		return nil, errors.New("credential acquisition failed")
	}

	tracked := &trackingReadCloser{Reader: strings.NewReader("")}
	resp := &http.Response{
		StatusCode: http.StatusProxyAuthRequired,
		Status:     "407 Proxy Authentication Required",
		Header:     http.Header{"Proxy-Authenticate": {"NTLM"}},
		Body:       tracked,
	}
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "http", Host: "example.test", Path: "/"}, Header: make(http.Header), Body: http.NoBody}
	s := &Server{cfg: config.Default()}

	got, err := s.retryHTTPProxyAuth(&http.Transport{}, req, req.URL, nil, req.URL.String(), "", "proxy.corp.example", resp)
	if got != nil {
		t.Fatalf("response=%v want nil", got)
	}
	if err == nil || !strings.Contains(err.Error(), "credential acquisition failed") {
		t.Fatalf("err=%v want explicit SSPI start failure", err)
	}
	if !tracked.closed {
		t.Fatal("407 body not closed on SSPI start failure")
	}
}
