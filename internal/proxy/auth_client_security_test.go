package proxy

import (
	"crypto/md5" // #nosec G501 -- test vectors exercise HTTP Digest compatibility.
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
)

func TestDigestRejectsURIThatDoesNotMatchRequestTarget(t *testing.T) {
	s := digestTestServer()
	remote := "127.0.0.1:51001"
	req := httptest.NewRequest(http.MethodGet, "http://example.test/good?x=1", nil)
	req.RemoteAddr = remote
	nonce := digestNonce(remote)
	req.Header.Set("Proxy-Authorization", digestTestHeader(http.MethodGet, "http://example.test/evil?x=1", nonce, "auth", "MD5"))
	if s.checkDigestClientAuth(req) {
		t.Fatal("digest auth accepted credentials bound to a different request-target")
	}
}

func TestDigestRequiresAdvertisedQopAuth(t *testing.T) {
	for _, qop := range []string{"", "auth-int"} {
		t.Run(fmt.Sprintf("qop=%q", qop), func(t *testing.T) {
			s := digestTestServer()
			remote := "127.0.0.1:51002"
			target := "http://example.test/resource"
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.RemoteAddr = remote
			req.Header.Set("Proxy-Authorization", digestTestHeader(req.Method, target, digestNonce(remote), qop, "MD5"))
			if s.checkDigestClientAuth(req) {
				t.Fatalf("digest auth accepted unsupported qop %q", qop)
			}
		})
	}
}

func TestDigestRejectsUnsupportedAlgorithm(t *testing.T) {
	s := digestTestServer()
	remote := "127.0.0.1:51003"
	target := "http://example.test/resource"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = remote
	req.Header.Set("Proxy-Authorization", digestTestHeader(req.Method, target, digestNonce(remote), "auth", "MD5-sess"))
	if s.checkDigestClientAuth(req) {
		t.Fatal("digest auth accepted unsupported algorithm MD5-sess")
	}
}

func TestDigestRejectsForgeableFutureNonceFromLegacyConstruction(t *testing.T) {
	remote := "127.0.0.1:51004"
	host := "127.0.0.1"
	ts := time.Now().Add(10 * time.Minute).Unix()
	salt := "0011223344556677"
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s:%s", ts, salt, host, digestRealm)))
	raw := fmt.Sprintf("%d:%s:%s", ts, salt, hex.EncodeToString(sum[:]))
	forged := base64.StdEncoding.EncodeToString([]byte(raw))
	if verifyDigestNonce(forged, remote) {
		t.Fatal("accepted forgeable future nonce using legacy unkeyed construction")
	}
}

func TestAuthenticatedConnectionAllowsZeroLengthBodyMethod(t *testing.T) {
	s := digestTestServer()
	remote := "127.0.0.1:51005"
	s.setClientAuthed(remote)
	req := httptest.NewRequest(http.MethodPost, "http://example.test/empty", nil)
	req.RemoteAddr = remote
	req.ContentLength = 0
	if !s.authenticateClient(req) {
		t.Fatal("authenticated connection rejected zero-length POST without redundant auth header")
	}
}

func TestParseNTLMAuthenticateRejectsOversizedResponseBeforeCrypto(t *testing.T) {
	const oversized = 8193
	msg := make([]byte, 64+oversized)
	copy(msg, "NTLMSSP\x00")
	binary.LittleEndian.PutUint32(msg[8:12], 3)
	binary.LittleEndian.PutUint16(msg[20:22], oversized)
	binary.LittleEndian.PutUint16(msg[22:24], oversized)
	binary.LittleEndian.PutUint32(msg[24:28], 64)
	if _, err := parseNTLMAuthenticateMessage(msg); err == nil {
		t.Fatal("oversized NT response reached verifier instead of being rejected by parser")
	}
}

func TestReadDERTLVRejectsOversizedSPNEGOToken(t *testing.T) {
	content := make([]byte, 16*1024+1)
	token := derTLV(0x60, content)
	if _, _, _, ok := readDERTLV(token); ok {
		t.Fatal("oversized SPNEGO DER token accepted")
	}
}

func digestTestServer() *Server {
	return &Server{cfg: config.Config{ClientUsername: "test", ClientPassword: "12345"}}
}

func digestTestHeader(method, uri, nonce, qop, algorithm string) string {
	if qop == "" {
		const (
			username = "test"
			password = "12345"
		)
		ha1 := digestTestMD5(username + ":" + digestRealm + ":" + password)
		ha2 := digestTestMD5(method + ":" + uri)
		response := digestTestMD5(ha1 + ":" + nonce + ":" + ha2)
		return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm="%s"`, username, digestRealm, nonce, uri, response, algorithm)
	}
	return digestTestHeaderFields(method, uri, nonce, qop, algorithm, "00000001", "abcdef")
}

func digestTestMD5(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestDigestRejectsSignedNonceBeyondFutureClockSkew(t *testing.T) {
	now := time.Now()
	remote := "127.0.0.1:51006"
	nonce := digestNonceAt(remote, now.Add(digestNonceFutureSkew+time.Second))
	if verifyDigestNonceAt(nonce, remote, now) {
		t.Fatal("accepted correctly signed nonce beyond allowed future clock skew")
	}
	withinSkew := digestNonceAt(remote, now.Add(digestNonceFutureSkew-time.Second))
	if !verifyDigestNonceAt(withinSkew, remote, now) {
		t.Fatal("rejected correctly signed nonce within allowed future clock skew")
	}
}

func TestDigestInvalidCredentialDoesNotPoisonReplayState(t *testing.T) {
	s := digestTestServer()
	remote := "127.0.0.1:51007"
	target := "http://example.test/resource"
	nonce := digestNonce(remote)
	valid := digestTestHeader(http.MethodGet, target, nonce, "auth", "MD5")
	invalid := strings.Replace(valid, `response="`, `response="0`, 1)

	reqBad := httptest.NewRequest(http.MethodGet, target, nil)
	reqBad.RemoteAddr = remote
	reqBad.Header.Set("Proxy-Authorization", invalid)
	if s.checkDigestClientAuth(reqBad) {
		t.Fatal("invalid digest unexpectedly authenticated")
	}
	if digestNonceSeen(nonce, "00000001") {
		t.Fatal("invalid digest poisoned replay state")
	}

	reqGood := httptest.NewRequest(http.MethodGet, target, nil)
	reqGood.RemoteAddr = remote
	reqGood.Header.Set("Proxy-Authorization", valid)
	if !s.checkDigestClientAuth(reqGood) {
		t.Fatal("valid digest rejected after invalid attempt with same nonce/nc")
	}
	if !digestNonceSeen(nonce, "00000001") {
		t.Fatal("verified digest was not recorded in replay state")
	}
}

func TestDigestConcurrentDuplicateAllowsExactlyOne(t *testing.T) {
	s := digestTestServer()
	remote := "127.0.0.1:51008"
	target := "http://example.test/resource"
	nonce := digestNonce(remote)
	header := digestTestHeader(http.MethodGet, target, nonce, "auth", "MD5")
	start := make(chan struct{})
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.RemoteAddr = remote
			req.Header.Set("Proxy-Authorization", header)
			<-start
			results <- s.checkDigestClientAuth(req)
		}()
	}
	close(start)
	accepted := 0
	for i := 0; i < 2; i++ {
		if <-results {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted=%d duplicate requests, want exactly one", accepted)
	}
}

func TestDigestRejectsMalformedNonceCountAndCnonce(t *testing.T) {
	cases := []struct {
		name   string
		nc     string
		cnonce string
	}{
		{name: "short nc", nc: "1", cnonce: "abcdef"},
		{name: "nonhex nc", nc: "00000xyz", cnonce: "abcdef"},
		{name: "empty cnonce", nc: "00000001", cnonce: ""},
		{name: "oversized cnonce", nc: "00000001", cnonce: strings.Repeat("x", maxDigestCnonceLen+1)},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := digestTestServer()
			remote := fmt.Sprintf("127.0.0.1:%d", 51100+i)
			target := "http://example.test/resource"
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.RemoteAddr = remote
			nonce := digestNonce(remote)
			req.Header.Set("Proxy-Authorization", digestTestHeaderFields(req.Method, target, nonce, "auth", "MD5", tc.nc, tc.cnonce))
			if s.checkDigestClientAuth(req) {
				t.Fatalf("accepted malformed digest nc=%q cnonce_len=%d", tc.nc, len(tc.cnonce))
			}
		})
	}
}

func TestNegotiateRejectsKerberosSPNEGOToken(t *testing.T) {
	s := digestTestServer()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.RemoteAddr = "127.0.0.1:51200"
	kerberosOID := []byte{0x2a, 0x86, 0x48, 0x86, 0xf7, 0x12, 0x01, 0x02, 0x02}
	mechList := derTLV(0xa0, derTLV(0x30, derTLV(0x06, kerberosOID)))
	mechToken := derTLV(0xa2, derTLV(0x04, []byte{0x01, 0x02, 0x03}))
	negTokenInit := derTLV(0xa0, derTLV(0x30, append(mechList, mechToken...)))
	token := derTLV(0x60, append(derTLV(0x06, spnegoOIDValue), negTokenInit...))
	req.Header.Set("Proxy-Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(token))
	if s.checkNTLMClientAuth(req, authSchemeNeg) {
		t.Fatal("NEGOTIATE accepted Kerberos/GSSAPI token even though downstream implementation is NTLM-over-SPNEGO only")
	}
}

func digestTestHeaderFields(method, uri, nonce, qop, algorithm, nc, cnonce string) string {
	const (
		username = "test"
		password = "12345"
	)
	ha1 := digestTestMD5(username + ":" + digestRealm + ":" + password)
	ha2 := digestTestMD5(method + ":" + uri)
	response := digestTestMD5(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm="%s", qop=%s, nc=%s, cnonce="%s"`, username, digestRealm, nonce, uri, response, algorithm, qop, nc, cnonce)
}

func FuzzReadDERTLVBounded(f *testing.F) {
	f.Add([]byte{0x30, 0x00})
	f.Add([]byte{0x60, 0x84, 0x7f, 0xff, 0xff, 0xff})
	f.Add(derTLV(0x04, []byte("abc")))
	f.Fuzz(func(t *testing.T, data []byte) {
		tag, content, rest, ok := readDERTLV(data)
		_ = tag
		if !ok {
			return
		}
		if len(data) > maxSPNEGOTokenLen {
			t.Fatalf("accepted oversized DER input len=%d", len(data))
		}
		if len(content)+len(rest)+2 > len(data)+5 {
			t.Fatalf("invalid DER partition content=%d rest=%d input=%d", len(content), len(rest), len(data))
		}
	})
}

func FuzzParseNTLMAuthenticateBounded(f *testing.F) {
	f.Add([]byte("NTLMSSP\x00"))
	seed := make([]byte, 64)
	copy(seed, "NTLMSSP\x00")
	binary.LittleEndian.PutUint32(seed[8:12], 3)
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := parseNTLMAuthenticateMessage(data)
		if err != nil {
			return
		}
		if len(data) > maxSPNEGOTokenLen || len(parsed.ntResponse) > maxNTLMResponseLen || len(parsed.lmResponse) > maxNTLMResponseLen {
			t.Fatalf("parser exceeded bounds: input=%d nt=%d lm=%d", len(data), len(parsed.ntResponse), len(parsed.lmResponse))
		}
	})
}

func BenchmarkDownstreamAuthRejectOversizedNTLM(b *testing.B) {
	const oversized = 8193
	msg := make([]byte, 64+oversized)
	copy(msg, "NTLMSSP\x00")
	binary.LittleEndian.PutUint32(msg[8:12], 3)
	binary.LittleEndian.PutUint16(msg[20:22], oversized)
	binary.LittleEndian.PutUint16(msg[22:24], oversized)
	binary.LittleEndian.PutUint32(msg[24:28], 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseNTLMAuthenticateMessage(msg); err == nil {
			b.Fatal("oversized NTLM response accepted")
		}
	}
}

func BenchmarkDownstreamAuthRejectKnownDigestReplay(b *testing.B) {
	s := digestTestServer()
	remote := "127.0.0.1:51300"
	target := "http://example.test/resource"
	nonce := digestNonce(remote)
	header := digestTestHeader(http.MethodGet, target, nonce, "auth", "MD5")
	if !digestNonceMarkIfNew(nonce, "00000001") {
		b.Fatal("failed to seed replay state")
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = remote
	req.Header.Set("Proxy-Authorization", header)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if s.checkDigestClientAuth(req) {
			b.Fatal("known replay authenticated")
		}
	}
}
