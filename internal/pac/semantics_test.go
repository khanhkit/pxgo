package pac

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func writeSemanticPAC(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "semantic.pac")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAPISS0005EncodingAliasesAndLegacyCharsets(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		marker   byte
		wantCode int
	}{
		{name: "documented latin1 alias", encoding: "latin1", marker: 0xe9, wantCode: 233},
		{name: "cp1252", encoding: "cp1252", marker: 0x80, wantCode: 8364},
		{name: "cp1251", encoding: "cp1251", marker: 0xdf, wantCode: 1071},
		{name: "auto cp1252 fallback", encoding: "auto", marker: 0x80, wantCode: 8364},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := []byte("function FindProxyForURL(url, host) { var marker = '")
			suffix := []byte(fmt.Sprintf("'; return marker.charCodeAt(0) === %d ? 'PROXY encoding.ok:8080' : 'DIRECT'; }", tt.wantCode))
			content := append(append(prefix, tt.marker), suffix...)
			p := New(writeSemanticPAC(t, content), tt.encoding)
			got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
			if err != nil {
				t.Fatalf("encoding %q failed: %v", tt.encoding, err)
			}
			if got != "encoding.ok:8080" {
				t.Fatalf("encoding %q result=%q", tt.encoding, got)
			}
		})
	}
}

func TestPXV012ContentTypeCharsetParsing(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        string
	}{
		{name: "simple", contentType: "application/x-ns-proxy-autoconfig; charset=utf-8", want: "utf-8"},
		{name: "quoted", contentType: `text/html; charset="windows-1251"`, want: "windows-1251"},
		{name: "uppercase key", contentType: "text/html; Charset=UTF-8", want: "UTF-8"},
		{name: "no charset", contentType: "application/x-ns-proxy-autoconfig", want: ""},
		{name: "empty input", contentType: "", want: ""},
		{name: "empty value", contentType: "text/html; charset=", want: ""},
		{name: "multiple params", contentType: "text/html; boundary=something; charset=iso-8859-1", want: "iso-8859-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contentTypeCharset(tt.contentType); got != tt.want {
				t.Fatalf("contentTypeCharset(%q)=%q want %q", tt.contentType, got, tt.want)
			}
		})
	}
}

func TestAPISS0005UTF16BOMEncoding(t *testing.T) {
	ascii := "function FindProxyForURL(url, host) { return 'PROXY utf16.ok:8080'; }"
	tests := []struct {
		name string
		bom  []byte
		emit func([]byte, byte) []byte
	}{
		{
			name: "little endian",
			bom:  []byte{0xff, 0xfe},
			emit: func(dst []byte, b byte) []byte { return append(dst, b, 0) },
		},
		{
			name: "big endian",
			bom:  []byte{0xfe, 0xff},
			emit: func(dst []byte, b byte) []byte { return append(dst, 0, b) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := make([]byte, 0, 2+2*len(ascii))
			data = append(data, tt.bom...)
			for i := 0; i < len(ascii); i++ {
				data = tt.emit(data, ascii[i])
			}
			p := New(writeSemanticPAC(t, data), "utf-16")
			got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
			if err != nil {
				t.Fatalf("utf-16 BOM failed: %v", err)
			}
			if got != "utf16.ok:8080" {
				t.Fatalf("result=%q", got)
			}
		})
	}
}

func TestAPISS0005MutableGlobalsAreSharedDeterministically(t *testing.T) {
	const callers = 8
	content := []byte(`
var counter = 0;
function FindProxyForURL(url, host) {
  var started = Date.now();
  while (Date.now() - started < 8) {}
  counter++;
  return "PROXY proxy" + counter + ".example.test:8080";
}`)
	p := New(writeSemanticPAC(t, content), "utf-8")
	if err := p.Load(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	results := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
			results <- got
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("PAC evaluation failed: %v", err)
		}
	}
	got := make([]string, 0, callers)
	for result := range results {
		got = append(got, result)
	}
	sort.Strings(got)
	want := make([]string, 0, callers)
	for i := 1; i <= callers; i++ {
		want = append(want, fmt.Sprintf("proxy%d.example.test:8080", i))
	}
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("mutable global results=%v want=%v", got, want)
	}
}

func TestAPISS0005TimeRangeIncludesLowerMinuteAndSecondBoundary(t *testing.T) {
	content := []byte(`
var RealDate = Date;
var fakeDates = [];
Date = function() { return new RealDate(fakeDates.shift()); };

function FindProxyForURL(url, host) {
  fakeDates = [
    RealDate.UTC(2026, 0, 1, 10, 20, 5, 0),
    RealDate.UTC(2026, 0, 1, 10, 20, 30, 0),
    RealDate.UTC(2026, 0, 1, 10, 20, 40, 0)
  ];
  if (!timeRange(10, 20, 10, 20, "GMT")) return "PROXY four-arg-boundary.failed:8080";

  fakeDates = [
    RealDate.UTC(2026, 0, 1, 10, 20, 30, 100),
    RealDate.UTC(2026, 0, 1, 10, 20, 30, 900),
    RealDate.UTC(2026, 0, 1, 10, 20, 30, 900)
  ];
  if (!timeRange(10, 20, 30, 10, 20, 30, "GMT")) return "PROXY six-arg-boundary.failed:8080";
  return "DIRECT";
}`)
	p := New(writeSemanticPAC(t, content), "utf-8")
	got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got != "DIRECT" {
		t.Fatalf("timeRange excluded lower boundary: %q", got)
	}
}

func TestAPISS0005MalformedPACDirectiveIsRejected(t *testing.T) {
	content := []byte(`function FindProxyForURL(url, host) { return "PROXY HTTP proxy.example.test:8080; DIRECT"; }`)
	p := New(writeSemanticPAC(t, content), "utf-8")
	if _, err := p.FindProxyForURLWithError("http://example.test", "example.test"); err == nil {
		t.Fatal("expected malformed nested PAC directive to fail")
	}
}

func TestAPISS0005PACResultCandidateCountIsBounded(t *testing.T) {
	var directives []string
	for i := 0; i < 64; i++ {
		directives = append(directives, fmt.Sprintf("PROXY proxy-%02d.example.test:8080", i))
	}
	content := []byte(`function FindProxyForURL(url, host) { return "` + strings.Join(directives, "; ") + `"; }`)
	p := New(writeSemanticPAC(t, content), "utf-8")
	if _, err := p.FindProxyForURLWithError("http://example.test", "example.test"); err == nil {
		t.Fatal("expected oversized PAC candidate list to fail")
	}
}

func TestAPISS0005PACResultLengthIsBounded(t *testing.T) {
	host := strings.Repeat("a", 20000) + ".example.test"
	content := []byte(`function FindProxyForURL(url, host) { return "PROXY ` + host + `:8080"; }`)
	p := New(writeSemanticPAC(t, content), "utf-8")
	if _, err := p.FindProxyForURLWithError("http://example.test", "example.test"); err == nil {
		t.Fatal("expected oversized PAC result to fail")
	}
}

func TestAPISS0005MyIPAddressIsDeterministicAndCachedPerGeneration(t *testing.T) {
	old := interfaceAddrs
	var calls atomic.Int32
	interfaceAddrs = func() ([]net.Addr, error) {
		calls.Add(1)
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("203.0.113.9"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("169.254.1.2"), Mask: net.CIDRMask(16, 32)},
			&net.IPNet{IP: net.ParseIP("192.168.50.20"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("10.8.0.4"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		}, nil
	}
	t.Cleanup(func() { interfaceAddrs = old })

	content := []byte(`function FindProxyForURL(url, host) { return "PROXY " + myIpAddress() + ":8080"; }`)
	p := New(writeSemanticPAC(t, content), "utf-8")
	if err := p.Load(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	for i := 0; i < 20; i++ {
		got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
		if err != nil {
			t.Fatal(err)
		}
		if got != "10.8.0.4:8080" {
			t.Fatalf("myIpAddress result=%q want=10.8.0.4:8080", got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("interface enumeration calls=%d want=1 per PAC generation", got)
	}
}

func TestAPISS0005CanonicalSystemResultBudget(t *testing.T) {
	if err := ValidateCanonicalResult("proxy.example.test:8080,DIRECT"); err != nil {
		t.Fatalf("valid canonical system result rejected: %v", err)
	}

	candidates := make([]string, maxPACCandidates+1)
	for i := range candidates {
		candidates[i] = fmt.Sprintf("proxy-%02d.example.test:8080", i)
	}
	if err := ValidateCanonicalResult(strings.Join(candidates, ",")); err == nil {
		t.Fatal("expected canonical system result candidate overflow to fail")
	}

	if err := ValidateCanonicalResult(strings.Repeat("x", maxPACResultBytes+1)); err == nil {
		t.Fatal("expected canonical system result length overflow to fail")
	}
}

func FuzzAPISS0005NormalizePACResultBounded(f *testing.F) {
	for _, seed := range []string{
		"DIRECT",
		"PROXY proxy.example.test:8080; DIRECT",
		"HTTPS secure.example.test:443; SOCKS5 socks.example.test:1080",
		"PROXY HTTP malformed.example.test:80",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, err := NormalizeResult(raw)
		if err != nil {
			return
		}
		if got == "" {
			t.Fatal("successful normalization returned empty result")
		}
		if strings.Contains(got, ";") {
			t.Fatalf("normalized result retained semicolon: %q", got)
		}
		if candidates := strings.Split(got, ","); len(candidates) > maxPACCandidates {
			t.Fatalf("normalized candidate count=%d > %d", len(candidates), maxPACCandidates)
		}
		if len(raw) > maxPACResultBytes {
			t.Fatalf("oversized raw result unexpectedly normalized: %d bytes", len(raw))
		}
	})
}

func TestAPISS0005PACDirectiveMappingIsTokenAware(t *testing.T) {
	content := []byte(`function FindProxyForURL(url, host) {
  return "HTTP http-proxy.example.test:80; HTTPS secure.example.test:443; SOCKS4 s4.example.test:1080; SOCKS5 s5.example.test:1080; SOCKS socks.example.test:1080; DIRECT";
}`)
	p := New(writeSemanticPAC(t, content), "utf-8")
	got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	want := "http-proxy.example.test:80,https://secure.example.test:443,socks4://s4.example.test:1080,socks5://s5.example.test:1080,socks5://socks.example.test:1080,DIRECT"
	if got != want {
		t.Fatalf("normalized=%q want=%q", got, want)
	}
}

func TestPXV012DefaultEncodingAutoDetectsLegacyPAC(t *testing.T) {
	prefix := []byte("function FindProxyForURL(url, host) { var marker = '")
	suffix := []byte("'; return marker.charCodeAt(0) === 8364 ? 'PROXY auto.default:8080' : 'DIRECT'; }")
	content := append(append(prefix, byte(0x80)), suffix...)
	p := New(writeSemanticPAC(t, content), "")
	got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got != "auto.default:8080" {
		t.Fatalf("default auto result=%q", got)
	}
}

func TestPXV012AutoEncodingHonorsHTTPContentTypeCharset(t *testing.T) {
	prefix := []byte("function FindProxyForURL(url, host) { var marker = '")
	suffix := []byte("'; return marker.charCodeAt(0) === 1040 ? 'PROXY charset.ok:8080' : 'DIRECT'; }")
	content := append(append(prefix, byte(0xc0)), suffix...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", `application/x-ns-proxy-autoconfig; charset="cp1251"`)
		_, _ = w.Write(content)
	}))
	defer server.Close()

	p := New(server.URL, "auto")
	got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got != "charset.ok:8080" {
		t.Fatalf("content-type charset result=%q", got)
	}
}

func TestPXV012AutoEncodingDetectsUTF32BOM(t *testing.T) {
	const script = "function FindProxyForURL(url, host) { return 'PROXY utf32.ok:8080'; }"
	for _, tc := range []struct {
		name string
		bom  []byte
		put  func([]byte, rune) []byte
	}{
		{
			name: "little endian",
			bom:  []byte{0xff, 0xfe, 0x00, 0x00},
			put: func(dst []byte, r rune) []byte {
				return append(dst, byte(r), byte(r>>8), byte(r>>16), byte(r>>24))
			},
		},
		{
			name: "big endian",
			bom:  []byte{0x00, 0x00, 0xfe, 0xff},
			put: func(dst []byte, r rune) []byte {
				return append(dst, byte(r>>24), byte(r>>16), byte(r>>8), byte(r))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append([]byte(nil), tc.bom...)
			for _, r := range script {
				data = tc.put(data, r)
			}
			p := New(writeSemanticPAC(t, data), "auto")
			got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
			if err != nil {
				t.Fatal(err)
			}
			if got != "utf32.ok:8080" {
				t.Fatalf("utf32 result=%q", got)
			}
		})
	}
}

func TestPXV012ExplicitASCIIEncoding(t *testing.T) {
	content := []byte(`function FindProxyForURL(url, host) { return "PROXY ascii.ok:8080"; }`)
	for _, encoding := range []string{"ascii", "us-ascii"} {
		t.Run(encoding, func(t *testing.T) {
			p := New(writeSemanticPAC(t, content), encoding)
			got, err := p.FindProxyForURLWithError("http://example.test", "example.test")
			if err != nil {
				t.Fatal(err)
			}
			if got != "ascii.ok:8080" {
				t.Fatalf("ascii result=%q", got)
			}
		})
	}
}

func TestPXV012ExplicitASCIIRejectsNonASCII(t *testing.T) {
	content := append([]byte(`function FindProxyForURL(url, host) { /* `), byte(0x80))
	content = append(content, []byte(` */ return "DIRECT"; }`)...)
	p := New(writeSemanticPAC(t, content), "ascii")
	if _, err := p.FindProxyForURLWithError("http://example.test", "example.test"); err == nil || !strings.Contains(err.Error(), "not valid ASCII") {
		t.Fatalf("explicit ASCII error=%v", err)
	}
}

func TestPXV012ContentTypeASCIIOverridesBOMDetection(t *testing.T) {
	content := append([]byte{0xef, 0xbb, 0xbf}, []byte(`function FindProxyForURL(url, host) { return "DIRECT"; }`)...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=ascii")
		_, _ = w.Write(content)
	}))
	defer server.Close()

	p := New(server.URL, "auto")
	if _, err := p.FindProxyForURLWithError("http://example.test", "example.test"); err == nil || !strings.Contains(err.Error(), "not valid ASCII") {
		t.Fatalf("Content-Type ASCII override error=%v", err)
	}
}
