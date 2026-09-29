package dnsresolver

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestAPISS0031ConfigGrammar(t *testing.T) {
	tests := []struct {
		raw      string
		wantMode string
		wantErr  bool
	}{
		{"", "system", false},
		{"system", "system", false},
		{"1.1.1.1", "dns", false},
		{"udp://1.1.1.1:5353", "dns", false},
		{"tcp://[2606:4700:4700::1111]:53", "dns", false},
		{"https://dns.example/dns-query", "doh", false},
		{"udp://1.1.1.1:53,https://dns.example/dns-query", "mixed", false},
		{"udp://resolver.example:53", "", true},
		{"https://dns.example", "", true},
		{"http://dns.example/dns-query", "", true},
		{"system,1.1.1.1", "", true},
		{"https://user:pass@dns.example/dns-query", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			p, err := New(tt.raw, time.Second)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("New(%q) error=nil", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Status().Mode; got != tt.wantMode {
				t.Fatalf("mode=%q want=%q", got, tt.wantMode)
			}
		})
	}
}

func TestAPISS0031CustomDNSOrderedFailover(t *testing.T) {
	addr, stop := startUDPFixture(t, net.IPv4(203, 0, 113, 7))
	defer stop()

	dead := unusedUDPAddr(t)
	p, err := New("udp://"+dead+",udp://"+addr, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ips, err := p.LookupIP(context.Background(), "fixture.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(203, 0, 113, 7)) {
		t.Fatalf("ips=%v", ips)
	}
	if got := p.Status().LastError; got != "" {
		t.Fatalf("last error=%q after successful failover", got)
	}
}

func TestAPISS0031CustomDNSCancellationIsBounded(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	p, err := New("udp://"+pc.LocalAddr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = p.LookupIP(ctx, "stall.test")
	if err == nil {
		t.Fatal("expected timeout")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lookup cancellation took %v", elapsed)
	}
	if got := p.Status().LastError; got != "timeout" {
		t.Fatalf("last error=%q want timeout", got)
	}
}

func TestAPISS0031DoHUsesDNSMessagePOSTAndValidatesResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/dns-message" {
			t.Errorf("content-type=%q", got)
		}
		query, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		response, err := fixtureResponse(query, net.IPv4(198, 51, 100, 9), false)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(response)
	}))
	defer server.Close()

	p, err := newPolicy(server.URL+"/dns-query", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ips, err := p.LookupIP(context.Background(), "doh.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(198, 51, 100, 9)) {
		t.Fatalf("ips=%v", ips)
	}
	if got := p.Status().Bootstrap; got != "" {
		t.Fatalf("IP-literal DoH endpoint bootstrap=%q", got)
	}
}

func TestAPISS0031DoHRejectsUnrelatedAnswer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		response, err := fixtureResponse(query, net.IPv4(192, 0, 2, 44), true)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(response)
	}))
	defer server.Close()
	p, err := newPolicy(server.URL+"/dns-query", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.LookupIP(context.Background(), "wanted.test"); err == nil {
		t.Fatal("expected unrelated answer to be rejected")
	}
	if got := p.Status().LastError; got != "dns-response" {
		t.Fatalf("last error=%q", got)
	}
}

func TestAPISS0031DoHDefaultTLSValidationRejectsFixtureCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dns-message")
	}))
	defer server.Close()
	p, err := New(server.URL+"/dns-query", 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.LookupIP(context.Background(), "tls.test"); err == nil {
		t.Fatal("expected normal TLS verification to reject httptest certificate")
	}
}

func TestAPISS0031ParseResponseRejectsMismatchedID(t *testing.T) {
	name, err := dnsName("id.test")
	if err != nil {
		t.Fatal(err)
	}
	query, id, err := buildQuery(name, dnsmessage.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	response, err := fixtureResponse(query, net.IPv4(192, 0, 2, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(response[:2], id+1)
	if _, _, err := parseResponse(response, id, name, dnsmessage.TypeA); err == nil {
		t.Fatal("expected mismatched id failure")
	}
}

func startUDPFixture(t *testing.T, answer net.IP) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, maxDNSMessageBytes)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			response, err := fixtureResponse(append([]byte(nil), buf[:n]...), answer, false)
			if err == nil {
				_, _ = pc.WriteTo(response, addr)
			}
		}
	}()
	return pc.LocalAddr().String(), func() {
		_ = pc.Close()
		<-done
	}
}

func unusedUDPAddr(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

func fixtureResponse(query []byte, answer net.IP, unrelated bool) ([]byte, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(query)
	if err != nil {
		return nil, err
	}
	questions, err := parser.AllQuestions()
	if err != nil || len(questions) != 1 {
		return nil, err
	}
	q := questions[0]
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		RecursionDesired:   header.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeSuccess,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(q); err != nil {
		return nil, err
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	name := q.Name
	if unrelated {
		name, err = dnsmessage.NewName("unrelated.test.")
		if err != nil {
			return nil, err
		}
	}
	rh := dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: 30}
	switch q.Type {
	case dnsmessage.TypeA:
		ip4 := answer.To4()
		if ip4 == nil {
			return nil, nil
		}
		var a [4]byte
		copy(a[:], ip4)
		if err := builder.AResource(rh, dnsmessage.AResource{A: a}); err != nil {
			return nil, err
		}
	case dnsmessage.TypeAAAA:
		// A successful empty AAAA answer is an authoritative NODATA response.
	default:
		return nil, nil
	}
	return builder.Finish()
}

func TestAPISS0031UDPTruncationFallsBackToTCP(t *testing.T) {
	addr, tcpCalls, stop := startTruncatedDNSFixture(t, net.IPv4(203, 0, 113, 88))
	defer stop()
	p, err := New("udp://"+addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ips, err := p.LookupIP(context.Background(), "truncated.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(203, 0, 113, 88)) {
		t.Fatalf("ips=%v", ips)
	}
	if got := tcpCalls.Load(); got == 0 {
		t.Fatal("truncated UDP response did not trigger TCP retry")
	}
}

func startTruncatedDNSFixture(t *testing.T, answer net.IP) (string, *atomic.Int32, func()) {
	t.Helper()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcpLn.Addr().(*net.TCPAddr).Port
	udpPC, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		tcpLn.Close()
		t.Fatal(err)
	}
	var tcpCalls atomic.Int32
	doneUDP := make(chan struct{})
	doneTCP := make(chan struct{})
	go func() {
		defer close(doneUDP)
		buf := make([]byte, maxDNSMessageBytes)
		for {
			n, addr, err := udpPC.ReadFrom(buf)
			if err != nil {
				return
			}
			response, err := fixtureResponse(append([]byte(nil), buf[:n]...), answer, false)
			if err == nil && len(response) >= 4 {
				response[2] |= 0x02 // DNS TC flag
				_, _ = udpPC.WriteTo(response, addr)
			}
		}
	}()
	go func() {
		defer close(doneTCP)
		for {
			conn, err := tcpLn.Accept()
			if err != nil {
				return
			}
			tcpCalls.Add(1)
			go func(conn net.Conn) {
				defer conn.Close()
				var length [2]byte
				if _, err := io.ReadFull(conn, length[:]); err != nil {
					return
				}
				n := int(binary.BigEndian.Uint16(length[:]))
				query := make([]byte, n)
				if _, err := io.ReadFull(conn, query); err != nil {
					return
				}
				response, err := fixtureResponse(query, answer, false)
				if err != nil {
					return
				}
				frame := make([]byte, 2+len(response))
				binary.BigEndian.PutUint16(frame[:2], uint16(len(response)))
				copy(frame[2:], response)
				_, _ = conn.Write(frame)
			}(conn)
		}
	}()
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tcpCalls, func() {
		_ = udpPC.Close()
		_ = tcpLn.Close()
		<-doneUDP
		<-doneTCP
	}
}

func TestAPISS0031DoHEndpointStatusDoesNotExposeQueryOrCredentials(t *testing.T) {
	for _, raw := range []string{
		"https://dns.example/dns-query?token=secret",
		"https://user:secret@dns.example/dns-query",
	} {
		if _, err := New(raw, time.Second); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("raw=%q err=%v", raw, err)
		}
	}
}
