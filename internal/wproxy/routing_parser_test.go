package wproxy

import (
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestParseProxyCanonicalIPv6AndValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []Server
		wantErr bool
	}{
		{name: "bracketed IPv6", input: "[2001:db8::1]:8080", want: []Server{{Host: "2001:db8::1", Port: 8080, Scheme: "http"}}},
		{name: "bare IPv6 default port", input: "2001:db8::2", want: []Server{{Host: "2001:db8::2", Port: 80, Scheme: "http"}}},
		{name: "HTTPS IPv6", input: "https://[2001:db8::3]:8443", want: []Server{{Host: "2001:db8::3", Port: 8443, Scheme: "https"}}},
		{name: "generic SOCKS compatibility", input: "socks://socks.example.com", want: []Server{{Host: "socks.example.com", Port: 1080, Scheme: "socks"}}},
		{name: "unsupported scheme", input: "ftp://proxy.example.com:21", wantErr: true},
		{name: "zero port", input: "proxy.example.com:0", wantErr: true},
		{name: "oversized port", input: "proxy.example.com:65536", wantErr: true},
		{name: "empty host", input: "http://:8080", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProxy(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseProxy(%q) unexpectedly succeeded: %#v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseProxy(%q): %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseProxy(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNoProxyCanonicalRules(t *testing.T) {
	proxy := []Server{{Host: "proxy.example.com", Port: 8080, Scheme: "http"}}

	wildcard, err := New(ModeConfig, proxy, "*.corp.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	assertDirect := func(t *testing.T, w *Wproxy, rawurl string, want bool) {
		t.Helper()
		servers, _, _, err := w.FindProxyForURL(rawurl)
		if err != nil {
			t.Fatal(err)
		}
		got := reflect.DeepEqual(servers, []Server{Direct})
		if got != want {
			t.Fatalf("FindProxyForURL(%q) direct=%v, want %v; servers=%#v", rawurl, got, want, servers)
		}
	}
	assertDirect(t, wildcard, "http://api.corp.example.com", true)
	assertDirect(t, wildcard, "http://corp.example.com", false)

	portRule, err := New(ModeConfig, proxy, "repo.corp.example:8443", "")
	if err != nil {
		t.Fatal(err)
	}
	assertDirect(t, portRule, "https://repo.corp.example:8443", true)
	assertDirect(t, portRule, "https://repo.corp.example:443", false)

	local, err := New(ModeConfig, proxy, "<local>", "")
	if err != nil {
		t.Fatal(err)
	}
	assertDirect(t, local, "http://printer", true)
	assertDirect(t, local, "http://[::1]", true)
}

func TestParseNoProxyRejectsMalformedNetworkTokens(t *testing.T) {
	for _, input := range []string{
		"10.0.0.0/999",
		"10.0.0.bad-10.0.1.0",
		"192.168.*.bad",
	} {
		t.Run(input, func(t *testing.T) {
			if _, _, err := ParseNoProxy(input, false); err == nil {
				t.Fatalf("ParseNoProxy(%q) unexpectedly succeeded", input)
			}
		})
	}

	_, hosts, err := ParseNoProxy("foo-bar.example.com,*.corp.example.com,repo.corp.example:8443", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"foo-bar.example.com", "*.corp.example.com", "repo.corp.example:8443"} {
		if !hosts[host] {
			t.Fatalf("missing canonical host rule %q in %#v", host, hosts)
		}
	}
}

func TestGetNetlocIPv6Authority(t *testing.T) {
	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	netloc, path, err := w.GetNetloc("[2001:db8::10]:443")
	if err != nil {
		t.Fatal(err)
	}
	if netloc != (Server{Host: "2001:db8::10", Port: 443}) || path != "/" {
		t.Fatalf("netloc=%#v path=%q", netloc, path)
	}
	if _, _, err := w.GetNetloc("example.com:70000"); err == nil {
		t.Fatal("expected out-of-range target port to fail")
	}
}

func TestNewRejectsMalformedEnvNoProxy(t *testing.T) {
	t.Setenv("http_proxy", "proxy.example.com:8080")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("no_proxy", "10.0.0.0/999")
	t.Setenv("NO_PROXY", "")
	if _, err := New(ModeNone, nil, "", ""); err == nil {
		t.Fatal("expected malformed environment no_proxy to fail")
	}
}

func FuzzParseProxyCanonical(f *testing.F) {
	for _, seed := range []string{
		"proxy.example.com:8080",
		"[2001:db8::1]:3128",
		"https://proxy.example.com",
		"ftp://proxy.example.com:21",
		"proxy.example.com:65536",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		servers, err := ParseProxy(raw)
		if err != nil {
			return
		}
		for _, server := range servers {
			if server == Direct {
				continue
			}
			if strings.TrimSpace(server.Host) == "" {
				t.Fatalf("successful parse produced empty host for %q", raw)
			}
			if server.Port < 1 || server.Port > 65535 {
				t.Fatalf("successful parse produced invalid port %d for %q", server.Port, raw)
			}
			switch server.Scheme {
			case "http", "https", "socks4", "socks4a", "socks5":
			default:
				t.Fatalf("successful parse produced unsupported scheme %q for %q", server.Scheme, raw)
			}
		}
	})
}

func FuzzParseNoProxyCanonical(f *testing.F) {
	for _, seed := range []string{
		"127.0.0.1,10.0.0.0/8",
		"*.corp.example.com",
		"repo.corp.example:8443",
		"10.0.0.0/999",
		"192.168.*.bad",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		set, hosts, err := ParseNoProxy(raw, false)
		if err != nil {
			return
		}
		_ = set.Contains(net.ParseIP("127.0.0.1"))
		for host := range hosts {
			if strings.Contains(host, "/") {
				t.Fatalf("network-looking token leaked into hostname rules: %q from %q", host, raw)
			}
		}
	})
}
