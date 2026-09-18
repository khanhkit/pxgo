package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"strings"
)

const pxgoVia = "1.1 pxgo"

var fixedHopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Connection",
	"TE",
	"Transfer-Encoding",
	"Upgrade",
}

func stripIntermediaryHeaders(header http.Header, stripProxyAuth bool) {
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if name := strings.TrimSpace(token); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range fixedHopByHopHeaders {
		header.Del(name)
	}
	for key := range header {
		if !strings.HasPrefix(strings.ToLower(key), "proxy-") {
			continue
		}
		if !stripProxyAuth && strings.EqualFold(key, "Proxy-Authenticate") {
			continue
		}
		header.Del(key)
	}
}

func appendVia(header http.Header) {
	header.Add("Via", pxgoVia)
}

func requestedUpgrade(req *http.Request) string {
	if req == nil || !headerContainsToken(req.Header, "Connection", "upgrade") {
		return ""
	}
	return strings.TrimSpace(req.Header.Get("Upgrade"))
}

func headerContainsToken(header http.Header, key, want string) bool {
	for _, value := range header.Values(key) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), want) {
				return true
			}
		}
	}
	return false
}

func withInformationalResponseForwarding(req *http.Request, rw http.ResponseWriter) *http.Request {
	if req == nil {
		return req
	}
	trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
		// 101 changes protocol ownership and is handled after RoundTrip returns.
		if code == http.StatusSwitchingProtocols {
			return nil
		}
		writeInformationalResponse(rw, code, http.Header(header))
		return nil
	}}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
}

func writeInformationalResponse(rw http.ResponseWriter, code int, header http.Header) {
	dst := rw.Header()
	saved := cloneHeader(dst)
	clearHeader(dst)
	clean := cloneHeader(header)
	stripIntermediaryHeaders(clean, true)
	appendVia(clean)
	copyHeader(dst, clean)
	rw.WriteHeader(code)
	clearHeader(dst)
	copyHeader(dst, saved)
}

func clearHeader(header http.Header) {
	for key := range header {
		header.Del(key)
	}
}

func (s *Server) writeHTTPResponse(rw http.ResponseWriter, resp *http.Response) {
	header := cloneHeader(resp.Header)
	preserveParentChallenge := resp.StatusCode == http.StatusProxyAuthRequired && strings.EqualFold(strings.TrimSpace(s.cfg.Auth), authNone)
	stripIntermediaryHeaders(header, !preserveParentChallenge)
	appendVia(header)
	for key := range resp.Trailer {
		if !headerContainsToken(header, "Trailer", key) {
			header.Add("Trailer", key)
		}
	}
	copyHeader(rw.Header(), header)
	rw.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(rw, resp.Body)
	for key, values := range resp.Trailer {
		rw.Header().Del(key)
		for _, value := range values {
			rw.Header().Add(key, value)
		}
	}
}

func (s *Server) handleHTTPUpgrade(rw http.ResponseWriter, req *http.Request, resp *http.Response) {
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok || requestedUpgrade(req) == "" {
		_ = resp.Body.Close()
		http.Error(rw, "invalid upstream protocol switch", http.StatusBadGateway)
		return
	}
	hijacker, ok := rw.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(rw, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	if !s.reserveTunnel() {
		_ = upstream.Close()
		http.Error(rw, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	client, brw, err := hijacker.Hijack()
	if err != nil {
		s.cancelTunnelReservation()
		_ = upstream.Close()
		return
	}
	tunnel, ok := s.activateManagedTunnel(client, upstream)
	if !ok {
		return
	}

	header := cloneHeader(resp.Header)
	upgrade := strings.TrimSpace(resp.Header.Get("Upgrade"))
	stripIntermediaryHeaders(header, true)
	header.Set("Connection", "Upgrade")
	if upgrade != "" {
		header.Set("Upgrade", upgrade)
	}
	appendVia(header)
	proto := resp.Proto
	if proto == "" {
		proto = "HTTP/1.1"
	}
	if _, err := fmt.Fprintf(brw, "%s %d %s\r\n", proto, resp.StatusCode, http.StatusText(resp.StatusCode)); err != nil {
		s.closeAndFinishTunnel(tunnel)
		return
	}
	if err := header.Write(brw); err != nil {
		s.closeAndFinishTunnel(tunnel)
		return
	}
	if _, err := io.WriteString(brw, "\r\n"); err != nil {
		s.closeAndFinishTunnel(tunnel)
		return
	}
	if err := brw.Flush(); err != nil {
		s.closeAndFinishTunnel(tunnel)
		return
	}
	if n := brw.Reader.Buffered(); n > 0 {
		if _, err := io.CopyN(upstream, brw.Reader, int64(n)); err != nil {
			s.closeAndFinishTunnel(tunnel)
			return
		}
	}
	go func() {
		defer s.finishTunnel(tunnel)
		relayUpgrade(client, upstream, tunnel)
	}()
}

func relayUpgrade(client io.ReadWriteCloser, upstream io.ReadWriteCloser, tunnel *managedTunnel) {
	done := make(chan struct{}, 2)
	copyOne := func(dst io.Writer, src io.Reader) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go copyOne(upstream, client)
	go copyOne(client, upstream)
	<-done
	tunnel.Close()
	<-done
}
