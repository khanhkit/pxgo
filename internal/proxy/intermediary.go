package proxy

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"time"
)

const (
	pxgoVia          = "1.1 pxgo"
	headerConnection = "Connection"
	headerKeepAlive  = "Keep-Alive"
	headerUpgrade    = "Upgrade"
)

var fixedHopByHopHeaders = []string{
	headerConnection,
	headerKeepAlive,
	"Proxy-Connection",
	"TE",
	"Transfer-Encoding",
	headerUpgrade,
}

func stripIntermediaryHeaders(header http.Header, stripProxyAuth bool) {
	for _, value := range header.Values(headerConnection) {
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
	if req == nil || !headerContainsToken(req.Header, headerConnection, "upgrade") {
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
	// A 200 SSE response has already passed post-authentication strict
	// long admission. The HTTP server's absolute WriteTimeout would otherwise
	// terminate a healthy long stream after a fixed duration. For genuinely
	// long SSE only, replace that deadline with a bounded deadline for EACH
	// actual downstream write/flush; leave it cleared between event frames.
	// Non-200 SSE-looking responses and non-flushing writers retain the
	// server's existing finite deadline and never get an exemption.
	sse := isSSEMediaType(resp.Header.Get("Content-Type"))
	flusher, canFlush := rw.(http.Flusher)
	window := configuredWriteTimeout(s.cfg)
	var control *http.ResponseController
	if sse && resp.StatusCode == http.StatusOK && canFlush && window > 0 {
		candidate := http.NewResponseController(rw)
		if err := candidate.SetWriteDeadline(time.Now().Add(window)); err == nil {
			control = candidate
		}
	}
	copyHeader(rw.Header(), header)
	rw.WriteHeader(resp.StatusCode)
	// SSE is determined by the actual upstream response, not a client's
	// Accept prediction. Flush headers and each returned body chunk.
	destination := io.Writer(rw)
	if sse && canFlush {
		if control != nil {
			if err := control.Flush(); err != nil {
				return
			}
			if err := control.SetWriteDeadline(time.Time{}); err != nil {
				return
			}
		} else {
			flusher.Flush()
		}
		destination = sseFlushingWriter{writer: rw, flusher: flusher, control: control, window: window}
	}
	_, _ = io.Copy(destination, resp.Body)
	for key, values := range resp.Trailer {
		rw.Header().Del(key)
		for _, value := range values {
			rw.Header().Add(key, value)
		}
	}
}

// sseFlushingWriter pushes each upstream SSE chunk without waiting for EOF.
type sseFlushingWriter struct {
	writer  io.Writer
	flusher http.Flusher
	control *http.ResponseController
	window  time.Duration
}

func (w sseFlushingWriter) Write(p []byte) (int, error) {
	if w.control != nil {
		if err := w.control.SetWriteDeadline(time.Now().Add(w.window)); err != nil {
			return 0, err
		}
	}
	n, err := w.writer.Write(p)
	if w.control != nil {
		if flushErr := w.control.Flush(); err == nil {
			err = flushErr
		}
		if err != nil {
			return n, err
		}
		return n, w.control.SetWriteDeadline(time.Time{})
	}
	w.flusher.Flush()
	return n, err
}

// isSSEMediaType matches actual response media type including parameters.
func isSSEMediaType(value string) bool {
	value = strings.TrimSpace(value)
	media, _, hasParams := strings.Cut(value, ";")
	if !strings.EqualFold(strings.TrimSpace(media), "text/event-stream") {
		// A cheap no-allocation fast path for ordinary JSON/HTML responses:
		// only actual SSE headers need strict MIME parameter validation.
		return false
	}
	if !hasParams {
		return true
	}
	parsed, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(parsed, "text/event-stream")
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
	header.Set(headerConnection, "Upgrade")
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
