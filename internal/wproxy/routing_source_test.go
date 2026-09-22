package wproxy

import (
	"errors"
	"reflect"
	"testing"

	"github.com/khanhkit/pxgo/internal/systemproxy"
)

type captureSystemResolver struct {
	result string
	err    error
	cfg    systemproxy.Config
	calls  int
}

func (r *captureSystemResolver) ResolveProxyForURL(rawurl string, cfg systemproxy.Config) (string, error) {
	r.calls++
	r.cfg = cfg
	return r.result, r.err
}

func (r *captureSystemResolver) Close() error { return nil }

func clearRoutingEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"http_proxy", "HTTP_PROXY",
		"https_proxy", "HTTPS_PROXY",
		"all_proxy", "ALL_PROXY",
		"no_proxy", "NO_PROXY",
	} {
		t.Setenv(key, "")
	}
}

func withSystemDiscovery(t *testing.T, cfg systemproxy.Config) {
	t.Helper()
	old := discoverSystemProxy
	discoverSystemProxy = func() systemproxy.Config { return cfg }
	t.Cleanup(func() { discoverSystemProxy = old })
}

func withSystemResolver(t *testing.T, resolver systemProxyResolver) {
	t.Helper()
	old := newSystemProxyResolver
	newSystemProxyResolver = func() (systemProxyResolver, error) { return resolver, nil }
	t.Cleanup(func() { newSystemProxyResolver = old })
}

func TestTCRouteREG001SystemPrecedesEnvironment(t *testing.T) {
	clearRoutingEnv(t)
	t.Setenv("HTTP_PROXY", "stale-env.example:3128")
	withSystemDiscovery(t, systemproxy.Config{
		Supported: true,
		Found:     true,
		ManualProxy: systemproxy.ManualProxyMap{
			Default: "system.example:8080",
		},
	})

	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if w.Source != RouteSourceSystemManual {
		t.Fatalf("Source = %q, want %q", w.Source, RouteSourceSystemManual)
	}
	got, _, _, err := w.FindProxyForURL("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	want := []Server{{Host: "system.example", Port: 8080, Scheme: "http"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("servers = %#v, want %#v", got, want)
	}
}

func TestTCRouteREG002EnvironmentUsesPerSchemeAndAllFallback(t *testing.T) {
	clearRoutingEnv(t)
	withSystemDiscovery(t, systemproxy.Config{Supported: false})
	t.Setenv("HTTP_PROXY", "http-env.example:8080")
	t.Setenv("HTTPS_PROXY", "https-env.example:8443")
	t.Setenv("ALL_PROXY", "socks5://all-env.example:1080")

	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if w.Source != RouteSourceEnvironment {
		t.Fatalf("Source = %q, want %q", w.Source, RouteSourceEnvironment)
	}
	tests := []struct {
		rawurl string
		want   []Server
	}{
		{"http://example.com/", []Server{{Host: "http-env.example", Port: 8080, Scheme: "http"}}},
		{"https://example.com/", []Server{{Host: "https-env.example", Port: 8443, Scheme: "http"}}},
		{"ftp://example.com/", []Server{{Host: "all-env.example", Port: 1080, Scheme: "socks5"}}},
	}
	for _, tc := range tests {
		got, _, _, err := w.FindProxyForURL(tc.rawurl)
		if err != nil {
			t.Fatalf("%s: %v", tc.rawurl, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s servers = %#v, want %#v", tc.rawurl, got, tc.want)
		}
	}
}

func TestTCRouteREG003ExplicitConfigSkipsAutomaticSources(t *testing.T) {
	clearRoutingEnv(t)
	t.Setenv("HTTP_PROXY", "env.example:3128")
	calls := 0
	old := discoverSystemProxy
	discoverSystemProxy = func() systemproxy.Config {
		calls++
		return systemproxy.Config{Supported: true, Found: true, AutoDetect: true}
	}
	t.Cleanup(func() { discoverSystemProxy = old })

	configured := []Server{{Host: "configured.example", Port: 9000, Scheme: "http"}}
	w, err := New(ModeConfig, configured, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if calls != 0 {
		t.Fatalf("system discovery calls = %d, want 0", calls)
	}
	if w.Source != RouteSourceConfig {
		t.Fatalf("Source = %q, want %q", w.Source, RouteSourceConfig)
	}
	got, _, _, err := w.FindProxyForURL("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, configured) {
		t.Fatalf("servers = %#v, want %#v", got, configured)
	}
}

func TestTCRouteNEG004SystemFailureDoesNotFallThroughToEnvironment(t *testing.T) {
	clearRoutingEnv(t)
	t.Setenv("HTTP_PROXY", "env.example:3128")
	withSystemDiscovery(t, systemproxy.Config{Supported: true, Found: true, AutoDetect: true})
	wantErr := errors.New("wpad unavailable")
	withSystemResolver(t, &captureSystemResolver{err: wantErr})

	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if w.Source != RouteSourceSystemAuto {
		t.Fatalf("Source = %q, want %q", w.Source, RouteSourceSystemAuto)
	}
	got, _, _, err := w.FindProxyForURL("http://example.com/")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if got != nil {
		t.Fatalf("servers = %#v, want nil on system failure", got)
	}
}

func TestTCRouteREG005AutoDetectAndPACStateReachResolverTogether(t *testing.T) {
	clearRoutingEnv(t)
	cfg := systemproxy.Config{
		Supported:  true,
		Found:      true,
		AutoDetect: true,
		IsPAC:      true,
		PACURL:     "http://wpad.example/proxy.pac",
	}
	withSystemDiscovery(t, cfg)
	resolver := &captureSystemResolver{result: "DIRECT"}
	withSystemResolver(t, resolver)

	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if w.Source != RouteSourceSystemAutoPAC {
		t.Fatalf("Source = %q, want %q", w.Source, RouteSourceSystemAutoPAC)
	}
	if _, _, _, err := w.FindProxyForURL("https://example.com/"); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	if !resolver.cfg.AutoDetect || !resolver.cfg.IsPAC || resolver.cfg.PACURL != cfg.PACURL {
		t.Fatalf("resolver config = %+v, want AutoDetect+PAC %q", resolver.cfg, cfg.PACURL)
	}
}

func TestTCRouteNEG006MalformedEnvironmentIsIgnoredWhenSystemSourceExists(t *testing.T) {
	clearRoutingEnv(t)
	t.Setenv("HTTP_PROXY", ":// malformed proxy")
	t.Setenv("NO_PROXY", "10.0.0.0/999")
	withSystemDiscovery(t, systemproxy.Config{
		Supported: true,
		Found:     true,
		ManualProxy: systemproxy.ManualProxyMap{
			Default: "system.example:8080",
		},
	})

	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatalf("lower-priority malformed environment must be ignored: %v", err)
	}
	defer w.Close()
	if w.Source != RouteSourceSystemManual {
		t.Fatalf("Source = %q, want system manual", w.Source)
	}
}

func TestTCRouteREG007SystemManualPreservesTargetScheme(t *testing.T) {
	clearRoutingEnv(t)
	withSystemDiscovery(t, systemproxy.Config{
		Supported: true,
		Found:     true,
		ManualProxy: systemproxy.ManualProxyMap{ByScheme: map[string]string{
			"http":  "http-system.example:8080",
			"https": "https-system.example:8443",
		}},
	})

	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tests := []struct {
		rawurl string
		want   Server
	}{
		{"http://example.com/", Server{Host: "http-system.example", Port: 8080, Scheme: "http"}},
		{"https://example.com/", Server{Host: "https-system.example", Port: 8443, Scheme: "http"}},
	}
	for _, tc := range tests {
		got, _, _, err := w.FindProxyForURL(tc.rawurl)
		if err != nil {
			t.Fatalf("%s: %v", tc.rawurl, err)
		}
		if !reflect.DeepEqual(got, []Server{tc.want}) {
			t.Fatalf("%s servers = %#v, want %#v", tc.rawurl, got, []Server{tc.want})
		}
	}
}

func TestTCRouteREG008MissingSchemeProxyFallsBackDirect(t *testing.T) {
	clearRoutingEnv(t)
	withSystemDiscovery(t, systemproxy.Config{Supported: false})
	t.Setenv("HTTP_PROXY", "http-env.example:8080")

	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	got, _, _, err := w.FindProxyForURL("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Server{Direct}) {
		t.Fatalf("servers = %#v, want DIRECT when HTTPS_PROXY/ALL_PROXY are absent", got)
	}
}

func TestTCRouteREG009UnsupportedNativePlatformIsExplicit(t *testing.T) {
	clearRoutingEnv(t)
	withSystemDiscovery(t, systemproxy.Config{Supported: false})

	w, err := New(ModeNone, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if w.SystemProxySupported {
		t.Fatal("SystemProxySupported = true, want false")
	}
	if w.Source != RouteSourceDirect {
		t.Fatalf("Source = %q, want %q", w.Source, RouteSourceDirect)
	}
}
