package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// newPersistentConnectTestProxy simulates a connection-bound authenticating
// proxy. Go's http.Server deliberately closes any rejected CONNECT connection
// in patched Go versions, so httptest.Server itself cannot model multi-round
// NTLM/Negotiate 407 challenges on one socket. Only the test fixture hijacks
// the socket; production connection and authentication rules are unchanged.
func newPersistentConnectTestProxy(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack test CONNECT: %v", err)
			return
		}
		defer conn.Close()
		for {
			writer := &persistentConnectResponse{header: make(http.Header)}
			handler.ServeHTTP(writer, req)
			if err := writer.writeTo(brw.Writer); err != nil {
				return
			}
			if err := brw.Flush(); err != nil {
				return
			}
			if req.Method == http.MethodConnect && writer.status >= 200 && writer.status < 300 {
				// The accepted CONNECT tunnel must transparently pass the actual
				// TLS handshake and body to the local HTTPS fixture.
				upstream, err := net.Dial("tcp", req.Host)
				if err != nil {
					return
				}
				defer upstream.Close()
				go func() {
					_, _ = io.Copy(upstream, brw.Reader)
					if tcp, ok := upstream.(*net.TCPConn); ok {
						_ = tcp.CloseWrite()
					}
				}()
				_, _ = io.Copy(conn, upstream)
				return
			}
			if req.Body != nil {
				_ = req.Body.Close()
			}
			req, err = http.ReadRequest(brw.Reader)
			if err != nil {
				if err != io.EOF {
					// Closing a successful CONNECT tunnel may produce a reset.
					return
				}
				return
			}
		}
	}))
	return parent
}

type persistentConnectResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *persistentConnectResponse) Header() http.Header { return w.header }
func (w *persistentConnectResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *persistentConnectResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

func (w *persistentConnectResponse) writeTo(out *bufio.Writer) error {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if _, err := fmt.Fprintf(out, "HTTP/1.1 %d %s\r\n", w.status, http.StatusText(w.status)); err != nil {
		return err
	}
	w.header.Set("Content-Length", strconv.Itoa(w.body.Len()))
	if err := w.header.Write(out); err != nil {
		return err
	}
	if _, err := out.WriteString("\r\n"); err != nil {
		return err
	}
	_, err := w.body.WriteTo(out)
	return err
}

// A raw CONNECT peer keeps challenged connections open for connection-bound
// auth; ordinary HTTP and PxGo parent authentication are covered elsewhere.
func newPersistentAuthConnectParent(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	return newPersistentConnectTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("expected CONNECT, got %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		auth := r.Header.Get("Proxy-Authorization")
		if strings.HasPrefix(auth, mode+" ") {
			w.WriteHeader(http.StatusOK)
			return
		}
		switch mode {
		case "Basic":
			w.Header().Set("Proxy-Authenticate", `Basic realm="PxClient"`)
		case "Digest":
			w.Header().Set("Proxy-Authenticate", `Digest realm="PxClient", nonce="test-nonce", qop="auth", algorithm="MD5"`)
		}
		http.Error(w, "auth required", http.StatusProxyAuthRequired)
	}))
}
