package proxy

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/kerberos"
)

func TestAPISS0019NewRequiresKerberosPrincipalBeforePACLoad(t *testing.T) {
	var hits atomic.Int32
	pacServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`function FindProxyForURL(url, host) { return "DIRECT"; }`))
	}))
	defer pacServer.Close()

	cfg := config.Default()
	cfg.PAC = pacServer.URL
	cfg.Kerberos = true

	s, err := New(cfg)
	if s != nil {
		t.Fatal("kerberos mode without principal returned a server")
	}
	if runtime.GOOS == goosWindows {
		if !errors.Is(err, kerberos.ErrProxyAuthUnsupported) {
			t.Fatalf("Windows err=%v, want ErrProxyAuthUnsupported", err)
		}
		if got := hits.Load(); got != 0 {
			t.Fatalf("Windows PAC fetches=%d want 0 before unsupported-mode rejection", got)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "--kerberos requires --username") {
		t.Fatalf("err=%v, want missing Kerberos username error", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("PAC fetches=%d want 1 before principal validation", got)
	}
}

func TestAPISS0019DiagnosticSnapshotReportsLastSuccessfulMechanism(t *testing.T) {
	cfg := config.Default()
	cfg.Server = "DIRECT"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	s.authMechanism.Record(authMechanismKerberos)
	snapshot := s.DiagnosticSnapshot()
	if got := snapshot.Auth.UpstreamMechanism; got != authMechanismKerberos {
		t.Fatalf("upstream mechanism=%q want %q", got, authMechanismKerberos)
	}
}

func TestAPISS0019HTTPAuthCommitsSelectedKerberosOnSuccess(t *testing.T) {
	oldCandidate := sspiSessionCandidate
	oldFactory := sspiSessionFactory
	t.Cleanup(func() {
		sspiSessionCandidate = oldCandidate
		sspiSessionFactory = oldFactory
	})

	initialHeader := apiss0019AdvertisedNegotiateHeader()
	selectedHeader := apiss0019SelectedKerberosHeader()
	continuationHeader := apiss0019GenericNegotiateResponseHeader()
	session := &fakeAuthSession{
		negotiateHeader: initialHeader,
		authHeader:      continuationHeader,
	}
	sspiSessionCandidate = func(_ config.Config, challenge string) bool {
		return authSchemeFromChallenge(challenge) == authNegotiate
	}
	sspiSessionFactory = func(_, _ string) (authSession, error) {
		return session, nil
	}

	var requests atomic.Int32
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n := requests.Add(1); n {
		case 1:
			if got := r.Header.Get("Proxy-Authorization"); got != initialHeader {
				t.Errorf("initial Proxy-Authorization=%q want %q", got, initialHeader)
			}
			w.Header().Set("Proxy-Authenticate", selectedHeader)
			w.WriteHeader(http.StatusProxyAuthRequired)
		case 2:
			if got := r.Header.Get("Proxy-Authorization"); got != continuationHeader {
				t.Errorf("continuation Proxy-Authorization=%q want %q", got, continuationHeader)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected proxy request %d", n)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer parent.Close()

	parentURL, err := url.Parse(parent.URL)
	if err != nil {
		t.Fatal(err)
	}
	targetURL, err := url.Parse("http://kerberos.example.test/resource")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(parentURL)}
	t.Cleanup(transport.CloseIdleConnections)

	cfg := config.Default()
	cfg.Auth = authNegotiate
	s := &Server{cfg: cfg}
	req := &http.Request{
		Method: http.MethodGet,
		URL:    targetURL,
		Header: make(http.Header),
		Body:   http.NoBody,
	}
	initialResp := &http.Response{
		StatusCode: http.StatusProxyAuthRequired,
		Status:     "407 Proxy Authentication Required",
		Header:     http.Header{"Proxy-Authenticate": {authSchemeNeg}},
		Body:       http.NoBody,
	}

	resp, err := s.retryHTTPProxyAuth(
		transport,
		req,
		targetURL,
		nil,
		targetURL.String(),
		parentURL.Hostname(),
		initialResp,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%s", resp.Status)
	}
	if got := s.authMechanism.Snapshot(); got != authMechanismKerberos {
		t.Fatalf("recorded mechanism=%q want %q", got, authMechanismKerberos)
	}
	if session.closeCount != 1 {
		t.Fatalf("session close count=%d want 1", session.closeCount)
	}
}

func apiss0019AdvertisedNegotiateHeader() string {
	mechList := append(
		derTLV(0x06, kerberosOIDValue),
		derTLV(0x06, ntlmOIDValue)...,
	)
	token := derTLV(0x60, append(
		derTLV(0x06, spnegoOIDValue),
		derTLV(0xa0, derTLV(0x30, derTLV(0xa0, derTLV(0x30, mechList))))...,
	))
	return authSchemeNeg + " " + base64.StdEncoding.EncodeToString(token)
}

func apiss0019SelectedKerberosHeader() string {
	token := derTLV(0xa1, derTLV(0x30,
		derTLV(0xa1, derTLV(0x06, kerberosOIDValue)),
	))
	return authSchemeNeg + " " + base64.StdEncoding.EncodeToString(token)
}

func apiss0019GenericNegotiateResponseHeader() string {
	token := derTLV(0xa1, derTLV(0x30,
		derTLV(0xa0, derTLV(0x0a, []byte{0x01})),
	))
	return authSchemeNeg + " " + base64.StdEncoding.EncodeToString(token)
}

func TestAPISS0019ConnectAuthCommitsSelectedKerberosOnSuccess(t *testing.T) {
	oldCandidate := sspiSessionCandidate
	oldFactory := sspiSessionFactory
	t.Cleanup(func() {
		sspiSessionCandidate = oldCandidate
		sspiSessionFactory = oldFactory
	})

	initialHeader := apiss0019AdvertisedNegotiateHeader()
	selectedHeader := apiss0019SelectedKerberosHeader()
	continuationHeader := apiss0019GenericNegotiateResponseHeader()
	session := &fakeAuthSession{
		negotiateHeader: initialHeader,
		authHeader:      continuationHeader,
	}
	sspiSessionCandidate = func(_ config.Config, challenge string) bool {
		return authSchemeFromChallenge(challenge) == authNegotiate
	}
	sspiSessionFactory = func(_, _ string) (authSession, error) {
		return session, nil
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errCh := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		expectedAuth := []string{"", initialHeader, continuationHeader}
		responses := []string{
			"HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: " + authSchemeNeg + "\r\nContent-Length: 0\r\n\r\n",
			"HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: " + selectedHeader + "\r\nContent-Length: 0\r\n\r\n",
			"HTTP/1.1 200 Connection Established\r\nContent-Length: 0\r\n\r\n",
		}
		for i := range responses {
			req, err := http.ReadRequest(reader)
			if err != nil {
				errCh <- err
				return
			}
			got := req.Header.Get("Proxy-Authorization")
			_ = req.Body.Close()
			if got != expectedAuth[i] {
				errCh <- &connectAuthHeaderError{got: got, want: expectedAuth[i]}
				return
			}
			if _, err := server.Write([]byte(responses[i])); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()

	cfg := config.Default()
	cfg.Auth = authNegotiate
	var recorded string
	leftover, err := sendUpstreamConnectWithAuthObserved(
		client,
		"kerberos.example.test:443",
		"proxy.corp.example",
		cfg,
		"",
		"",
		nil,
		func(mechanism string) { recorded = mechanism },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if len(leftover) != 0 {
		t.Fatalf("leftover=%x want empty", leftover)
	}
	if recorded != authMechanismKerberos {
		t.Fatalf("recorded mechanism=%q want %q", recorded, authMechanismKerberos)
	}
	if session.closeCount != 1 {
		t.Fatalf("session close count=%d want 1", session.closeCount)
	}
}

type connectAuthHeaderError struct {
	got  string
	want string
}

func (e *connectAuthHeaderError) Error() string {
	return "Proxy-Authorization mismatch: got " + e.got + " want " + e.want
}

func TestAPISS0019UnixKerberosHTTPUsesSPNEGOSession(t *testing.T) {
	oldCandidate := kerberosSessionCandidate
	oldFactory := kerberosSessionFactory
	t.Cleanup(func() {
		kerberosSessionCandidate = oldCandidate
		kerberosSessionFactory = oldFactory
	})

	initialHeader := apiss0019AdvertisedNegotiateHeader()
	session := &fakeAuthSession{negotiateHeader: initialHeader}
	kerberosSessionCandidate = func(cfg config.Config, challenge string) bool {
		return cfg.Kerberos && authSchemeFromChallenge(challenge) == authNegotiate
	}
	kerberosSessionFactory = func(_ *kerberos.Manager, _ string) (authSession, error) {
		return session, nil
	}

	var requests atomic.Int32
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			if got := r.Header.Get("Proxy-Authorization"); got != initialHeader {
				t.Errorf("Proxy-Authorization=%q want Kerberos SPNEGO token", got)
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer parent.Close()
	parentURL, err := url.Parse(parent.URL)
	if err != nil {
		t.Fatal(err)
	}
	targetURL, _ := url.Parse("http://kerberos.example.test/resource")
	transport := &http.Transport{Proxy: http.ProxyURL(parentURL)}
	t.Cleanup(transport.CloseIdleConnections)

	cfg := config.Default()
	cfg.Auth = authNegotiate
	cfg.Kerberos = true
	s := &Server{cfg: cfg}
	req := &http.Request{Method: http.MethodGet, URL: targetURL, Header: make(http.Header), Body: http.NoBody}
	initialResp := &http.Response{
		StatusCode: http.StatusProxyAuthRequired,
		Status:     "407 Proxy Authentication Required",
		Header:     http.Header{"Proxy-Authenticate": {authSchemeNeg}},
		Body:       http.NoBody,
	}
	resp, err := s.retryHTTPProxyAuth(transport, req, targetURL, nil, targetURL.String(), parentURL.Hostname(), initialResp)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%s", resp.Status)
	}
	if got := s.authMechanism.Snapshot(); got != authMechanismKerberos {
		t.Fatalf("mechanism=%q want Kerberos", got)
	}
	if session.closeCount != 1 {
		t.Fatalf("session close count=%d want 1", session.closeCount)
	}
}

func TestAPISS0019UnixKerberosConnectUsesSPNEGOSession(t *testing.T) {
	oldCandidate := kerberosSessionCandidate
	oldFactory := kerberosSessionFactory
	t.Cleanup(func() {
		kerberosSessionCandidate = oldCandidate
		kerberosSessionFactory = oldFactory
	})

	initialHeader := apiss0019AdvertisedNegotiateHeader()
	session := &fakeAuthSession{negotiateHeader: initialHeader}
	kerberosSessionCandidate = func(cfg config.Config, challenge string) bool {
		return cfg.Kerberos && authSchemeFromChallenge(challenge) == authNegotiate
	}
	kerberosSessionFactory = func(_ *kerberos.Manager, _ string) (authSession, error) {
		return session, nil
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	errCh := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		for i, response := range []string{
			"HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Negotiate\r\nContent-Length: 0\r\n\r\n",
			"HTTP/1.1 200 Connection Established\r\nContent-Length: 0\r\n\r\n",
		} {
			req, err := http.ReadRequest(reader)
			if err != nil {
				errCh <- err
				return
			}
			if i == 1 && req.Header.Get("Proxy-Authorization") != initialHeader {
				errCh <- fmt.Errorf("second CONNECT did not carry Kerberos SPNEGO header")
				return
			}
			if _, err := server.Write([]byte(response)); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()

	cfg := config.Default()
	cfg.Auth = authNegotiate
	cfg.Kerberos = true
	s := &Server{cfg: cfg}
	if _, err := s.sendUpstreamConnect(client, "origin.example.test:443", "proxy.example.test", ""); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if got := s.authMechanism.Snapshot(); got != authMechanismKerberos {
		t.Fatalf("mechanism=%q want Kerberos", got)
	}
	if session.closeCount != 1 {
		t.Fatalf("session close count=%d want 1", session.closeCount)
	}
}
