package proxy

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

const capacityWebSocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func capacityWebSocketFrame(c net.Conn, payload []byte, seed byte, toServer bool) error {
	if len(payload) > 125 {
		return fmt.Errorf("test frame exceeds simple bounded payload")
	}
	frame := []byte{0x82, byte(len(payload))}
	if toServer {
		frame[1] |= 0x80
		mask := []byte{seed, seed ^ 0x5a, seed ^ 0xa5, seed ^ 0xff}
		frame = append(frame, mask...)
		for i, b := range payload {
			frame = append(frame, b^mask[i%4])
		}
	} else {
		frame = append(frame, payload...)
	}
	_, err := c.Write(frame)
	return err
}

func capacityReadWebSocketFrame(reader *bufio.Reader, fromClient bool) ([]byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(reader, head[:]); err != nil {
		return nil, err
	}
	if head[0] != 0x82 || head[1]&0x7f > 125 {
		return nil, fmt.Errorf("invalid websocket binary frame header: %x", head)
	}
	masked := head[1]&0x80 != 0
	if masked != fromClient {
		return nil, fmt.Errorf("websocket masking direction mismatch: %x", head)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, int(head[1]&0x7f))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, nil
}

func capacityWSOriginHandle(conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return fmt.Errorf("upgrade origin request: %w", err)
	}
	defer req.Body.Close()
	if req.Method != http.MethodGet || !intermediaryHeaderHasToken(req.Header, "Connection", "upgrade") ||
		!strings.EqualFold(req.Header.Get("Upgrade"), "websocket") ||
		req.Header.Get("Sec-WebSocket-Version") != "13" {
		return fmt.Errorf("invalid upgrade request headers")
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return fmt.Errorf("invalid websocket client key: %w", err)
	}
	if len(decoded) != 16 {
		return fmt.Errorf("invalid websocket client key length %d", len(decoded))
	}
	sum := sha1.Sum([]byte(key + capacityWebSocketGUID)) // #nosec G401 -- RFC 6455 handshake mandates SHA-1, not for authentication
	accept := base64.StdEncoding.EncodeToString(sum[:])
	if _, err = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		data, e := capacityReadWebSocketFrame(reader, true)
		if e != nil {
			return e
		}
		if e = capacityWebSocketFrame(conn, data, 0, false); e != nil {
			return e
		}
	}
	return nil
}

func capacityWSClientOpen(proxy, origin string, index int) (net.Conn, *bufio.Reader, error) {
	c, err := net.DialTimeout("tcp", proxy, 4*time.Second)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = c.Close()
		}
	}()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	keyBytes := []byte(fmt.Sprintf("%016d", index))
	key := base64.StdEncoding.EncodeToString(keyBytes)
	target := "http://" + origin + "/ws"
	if _, err = fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n", target, origin, key); err != nil {
		return nil, nil, err
	}
	reader := bufio.NewReader(c)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols ||
		!intermediaryHeaderHasToken(resp.Header, "Connection", "upgrade") ||
		!strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("unexpected upgrade response: %s", resp.Status)
	}
	sum := sha1.Sum([]byte(key + capacityWebSocketGUID)) // #nosec G401 -- required WebSocket protocol handshake
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("invalid websocket accept handshake")
	}
	_ = resp.Body.Close()
	ok = true
	return c, reader, nil
}

func capacityWSRoundTrip(c net.Conn, reader *bufio.Reader, index, round int) error {
	payload := []byte(fmt.Sprintf("ws-binary-client=%02d round=%d", index, round))
	if err := capacityWebSocketFrame(c, payload, byte(index+round+1), true); err != nil {
		return err
	}
	got, err := capacityReadWebSocketFrame(reader, false)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("websocket frame payload mismatch %q != %q", got, payload)
	}
	return nil
}

// Real RFC6455 masked client binary frames, 101 upgrade and bidirectional
// payload, eight physically held WS tunnels plus eight CONNECT echo tunnels.
// Short HTTP must reach global acquire at true 16/16 physical saturation,
// block, then borrow a freed physical permit without killing admitted WS.
//
//nolint:gocyclo // Opt-in real-socket test keeps ownership/failure gates explicit.
func TestCapacityMixedWebSocketSaturated(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_WS_BENCH") != "1" {
		t.Skip("opt-in native WebSocket saturated admission research")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("AUTO mode required for WebSocket capacity test")
	}
	const websocketN, connectN, hardCap = 8, 8, 16
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var acceptWG, workerWG sync.WaitGroup
	originErrors := make(chan error, websocketN)
	acceptWG.Add(1)
	go func() {
		defer acceptWG.Done()
		for {
			c, e := origin.Accept()
			if e != nil {
				return
			}
			workerWG.Add(1)
			go func(c net.Conn) {
				defer workerWG.Done()
				originErrors <- capacityWSOriginHandle(c)
			}(c)
		}
	}()
	ws := make([]net.Conn, websocketN)
	readers := make([]*bufio.Reader, websocketN)
	defer func() {
		for _, c := range ws {
			if c != nil {
				_ = c.Close()
			}
		}
		_ = origin.Close()
		acceptWG.Wait()
		workerWG.Wait()
	}()

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var echoAccept, echoWorkers sync.WaitGroup
	echoAccept.Add(1)
	go func() {
		defer echoAccept.Done()
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			echoWorkers.Add(1)
			go func(c net.Conn) {
				defer echoWorkers.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	connects := make([]net.Conn, connectN)
	defer func() {
		for _, c := range connects {
			if c != nil {
				_ = c.Close()
			}
		}
		_ = echo.Close()
		echoAccept.Wait()
		echoWorkers.Wait()
	}()

	httpOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "mixed-ok")
	}))
	defer httpOrigin.Close()
	cfg := config.Default()
	cfg.MaxConnections = hardCap
	cfg.ProxyReload = 0
	cfg.Idle = 40
	cfg.SockTimeout = 15
	px := startTestProxy(t, cfg)
	proxy := px.ListenAddr()

	for i := range ws {
		c, reader, e := capacityWSClientOpen(proxy, origin.Addr().String(), i)
		if e != nil {
			t.Fatalf("websocket upgrade[%d]: %v", i, e)
		}
		ws[i], readers[i] = c, reader
		if e = capacityWSRoundTrip(c, reader, i, 0); e != nil {
			t.Fatalf("websocket first frame[%d]: %v", i, e)
		}
	}
	for i := range connects {
		c, e := capacityMixedConnect(proxy, echo.Addr().String(), i)
		if e != nil {
			t.Fatalf("CONNECT[%d]: %v", i, e)
		}
		connects[i] = c
	}
	s := px.capacityDiagnosticSnapshot()
	if s.ActiveDownstream != hardCap || s.EffectiveLimit != hardCap {
		t.Fatalf("global physical permits not saturated: %+v", s)
	}
	if s.ClassPools != nil && (s.ClassPools.Long != hardCap || s.ClassPools.Short != 0 ||
		s.ClassPools.Provisional != 0 || s.ClassPools.Total != hardCap) {
		t.Fatalf("WebSocket class ledger invalid: %+v", s.ClassPools)
	}
	waitBefore := s.AdmissionWaits
	dialed := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		c, e := net.DialTimeout("tcp", proxy, 4*time.Second)
		dialed <- e
		if e == nil {
			e = capacityMixedHTTP(c, bufio.NewReader(c), httpOrigin.URL, false)
			_ = c.Close()
		}
		done <- e
	}()
	select {
	case e := <-dialed:
		if e != nil {
			t.Fatalf("short TCP dial: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("short dial did not reach proxy listener")
	}
	waitDeadline := time.Now().Add(5 * time.Second)
	waitObserved := false
	for time.Now().Before(waitDeadline) {
		s = px.capacityDiagnosticSnapshot()
		if s.ActiveDownstream == hardCap && s.AdmissionWaits > waitBefore {
			waitObserved = true
			break
		}
		select {
		case e := <-done:
			t.Fatalf("short prematurely completed before acquire wait: %v", e)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waitObserved {
		t.Fatal("no observed short admission wait under physical cap")
	}
	select {
	case e := <-done:
		t.Fatalf("short prematurely completed at physical cap: %v", e)
	case <-time.After(150 * time.Millisecond):
	}
	_ = connects[0].Close()
	connects[0] = nil
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("short failed to borrow released permit: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("short starved after long CONNECT released a permit")
	}
	replacement, err := capacityMixedConnect(proxy, echo.Addr().String(), 0)
	if err != nil {
		t.Fatalf("released global permit could not be replenished: %v", err)
	}
	connects[0] = replacement
	for i := range ws {
		if err := capacityWSRoundTrip(ws[i], readers[i], i, 1); err != nil {
			t.Fatalf("WebSocket[%d] frame after borrow: %v", i, err)
		}
	}
	for i, c := range connects {
		if e := verifyCapacityHeldTunnel(c, i); e != nil {
			t.Fatalf("CONNECT[%d] after WebSocket borrowing: %v", i, e)
		}
	}
	for i, c := range ws {
		_ = c.Close()
		ws[i] = nil
	}
	for i := 0; i < websocketN; i++ {
		if e := <-originErrors; e != nil {
			t.Fatalf("WebSocket origin[%d] error: %v", i, e)
		}
	}
	for i, c := range connects {
		_ = c.Close()
		connects[i] = nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s = px.capacityDiagnosticSnapshot()
		if s.ActiveDownstream == 0 && (s.ClassPools == nil || s.ClassPools.Total == 0) {
			t.Logf("WS_MIXED_RESULT websocket=%d connect=%d short_borrow=1 wait_delta=%d global_active=0 classes=%t", websocketN, connectN, s.AdmissionWaits-waitBefore, s.ClassPools != nil)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("global/class physical permits leaked: %+v", s)
}
