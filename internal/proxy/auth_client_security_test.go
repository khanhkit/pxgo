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
	const (
		username = "test"
		password = "12345"
		nc       = "00000001"
		cnonce   = "abcdef"
	)
	ha1 := digestTestMD5(username + ":" + digestRealm + ":" + password)
	ha2 := digestTestMD5(method + ":" + uri)
	var response string
	if qop == "" {
		response = digestTestMD5(ha1 + ":" + nonce + ":" + ha2)
	} else {
		response = digestTestMD5(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	}
	parts := []string{
		fmt.Sprintf(`username="%s"`, username),
		fmt.Sprintf(`realm="%s"`, digestRealm),
		fmt.Sprintf(`nonce="%s"`, nonce),
		fmt.Sprintf(`uri="%s"`, uri),
		fmt.Sprintf(`response="%s"`, response),
		fmt.Sprintf(`algorithm="%s"`, algorithm),
	}
	if qop != "" {
		parts = append(parts, "qop="+qop, "nc="+nc, fmt.Sprintf(`cnonce="%s"`, cnonce))
	}
	return "Digest " + strings.Join(parts, ", ")
}

func digestTestMD5(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
