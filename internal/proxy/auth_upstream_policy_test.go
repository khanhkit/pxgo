package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/pavelsimo/pxgo/internal/config"
)

func TestDefaultUpstreamAuthWithCredentialsDoesNotDowngradeToBasic(t *testing.T) {
	cfg := upstreamPolicyConfig("")
	got := UpstreamProxyAuthHeader(cfg, http.MethodGet, "http://origin.example.test/resource", []string{`Basic realm="parent"`})
	if got != "" {
		t.Fatalf("default auth downgraded reusable credentials to Basic: %q", got)
	}
}

func TestExplicitAnyCanOptIntoBasicFallback(t *testing.T) {
	cfg := upstreamPolicyConfig("ANY")
	got := UpstreamProxyAuthHeader(cfg, http.MethodGet, "http://origin.example.test/resource", []string{`Basic realm="parent"`})
	if !strings.HasPrefix(got, "Basic ") {
		t.Fatalf("explicit ANY should retain documented Basic fallback opt-in, got %q", got)
	}
}

func TestSingleBasicRequiresMatchingChallenge(t *testing.T) {
	cfg := upstreamPolicyConfig("BASIC")
	for _, tc := range []struct {
		name       string
		challenges []string
	}{
		{name: "no challenge"},
		{name: "digest challenge", challenges: []string{`Digest realm="parent", nonce="abc", qop="auth"`}},
		{name: "ntlm challenge", challenges: []string{"NTLM"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := UpstreamProxyAuthHeader(cfg, http.MethodGet, "http://origin.example.test/resource", tc.challenges)
			if got != "" {
				t.Fatalf("Basic credentials emitted without matching Basic challenge: %q", got)
			}
		})
	}
	if got := UpstreamProxyAuthHeader(cfg, http.MethodGet, "http://origin.example.test/resource", []string{`Basic realm="parent"`}); !strings.HasPrefix(got, "Basic ") {
		t.Fatalf("matching Basic challenge did not produce credentials: %q", got)
	}
}

func TestUpstreamDigestRejectsUnsupportedQop(t *testing.T) {
	cfg := upstreamPolicyConfig("DIGEST")
	for _, qop := range []string{"auth-int", "auth-conf"} {
		t.Run(qop, func(t *testing.T) {
			challenge := fmt.Sprintf(`Digest realm="parent", nonce="abc", qop="%s", algorithm=MD5`, qop)
			if got := UpstreamProxyAuthHeader(cfg, http.MethodGet, "http://origin.example.test/resource", []string{challenge}); got != "" {
				t.Fatalf("unsupported Digest qop %q produced Authorization: %q", qop, got)
			}
		})
	}
}

func TestUpstreamDigestRejectsUnsupportedAlgorithm(t *testing.T) {
	cfg := upstreamPolicyConfig("DIGEST")
	for _, algorithm := range []string{"MD5-sess", "SHA-256", "SHA-256-sess"} {
		t.Run(algorithm, func(t *testing.T) {
			challenge := fmt.Sprintf(`Digest realm="parent", nonce="abc", qop="auth", algorithm=%s`, algorithm)
			if got := UpstreamProxyAuthHeader(cfg, http.MethodGet, "http://origin.example.test/resource", []string{challenge}); got != "" {
				t.Fatalf("unsupported Digest algorithm %q produced Authorization: %q", algorithm, got)
			}
		})
	}
}

func TestUpstreamDigestRetainsLegacyNoQopMD5Compatibility(t *testing.T) {
	cfg := upstreamPolicyConfig("DIGEST")
	for _, challenge := range []string{
		`Digest realm="parent", nonce="abc"`,
		`Digest realm="parent", nonce="abc", algorithm=MD5`,
		`Digest realm="parent", nonce="abc", algorithm=md5`,
	} {
		if got := UpstreamProxyAuthHeader(cfg, http.MethodGet, "http://origin.example.test/resource", []string{challenge}); !strings.HasPrefix(got, "Digest ") {
			t.Fatalf("supported legacy/MD5 Digest challenge rejected: %q -> %q", challenge, got)
		}
	}
}

func TestUpstreamDigestAuthSchemeIsCaseInsensitive(t *testing.T) {
	cfg := upstreamPolicyConfig("DIGEST")
	challenge := `digest realm="parent", nonce="abc", qop="auth", algorithm=MD5`
	if got := UpstreamProxyAuthHeader(cfg, http.MethodGet, "http://origin.example.test/resource", []string{challenge}); !strings.HasPrefix(got, "Digest ") {
		t.Fatalf("lowercase Digest auth-scheme rejected: %q", got)
	}
}

func TestDefaultAuthStrictProxyNeverReceivesBasicCredentials(t *testing.T) {
	var mu sync.Mutex
	var authHeaders []string
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Proxy-Authorization"))
		mu.Unlock()
		w.Header().Set("Proxy-Authenticate", `Basic realm="parent"`)
		http.Error(w, "auth required", http.StatusProxyAuthRequired)
	}))
	defer parent.Close()
	parentURL, err := url.Parse(parent.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server = parentURL.Host
	cfg.Username = "reusable-user"
	cfg.Password = "reusable-secret"
	child := startTestProxy(t, cfg)
	resp, err := proxyClient(t, child.Port()).Get("http://origin.example.test/resource")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status=%s want upstream 407", resp.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, auth := range authHeaders {
		if auth != "" {
			t.Fatalf("request %d leaked default credentials to Basic-only proxy: %q", i+1, auth)
		}
	}
}

func TestChallengeParserKeepsQuotedCommaAndFailsClosedOnUnclosedQuote(t *testing.T) {
	valid := `Digest realm="a,b", nonce="abc", qop="auth", Basic realm="fallback"`
	parts := splitProxyAuthenticateValues(valid)
	if len(parts) != 2 || authSchemeFromChallenge(parts[0]) != authDigest || authSchemeFromChallenge(parts[1]) != authBasic {
		t.Fatalf("quoted comma split=%q", parts)
	}

	malformed := `Digest realm="unterminated, Basic realm="fallback"`
	parts = splitProxyAuthenticateValues(malformed)
	for _, part := range parts {
		if authSchemeFromChallenge(part) == authBasic {
			t.Fatalf("malformed quoted challenge fabricated Basic fallback: %q", parts)
		}
	}
}

func upstreamPolicyConfig(auth string) config.Config {
	cfg := config.Default()
	cfg.Auth = auth
	cfg.Username = "reusable-user"
	cfg.Password = "reusable-secret"
	return cfg
}
