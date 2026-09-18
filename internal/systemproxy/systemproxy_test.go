package systemproxy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseManualProxyString(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"proxy.example.com:8080", "proxy.example.com:8080"},
		{"http=proxy1:8080;https=proxy2:8443", "proxy1:8080,proxy2:8443"},
		{"http=proxy1:8080; ftp=ftp-proxy:21; https=proxy2:8443", "proxy1:8080,proxy2:8443"},
		{"http=proxy1:8080; socks=socks-proxy:1080; ftp=ftp-proxy:21", "proxy1:8080,socks5://socks-proxy:1080"},
		{"http=proxy:8080;https=proxy:8080", "proxy:8080"},
		{"proxy1:8080 proxy2:8080;proxy3:8080,proxy4:8080", "proxy1:8080,proxy2:8080,proxy3:8080,proxy4:8080"},
	}
	for _, tt := range tests {
		if got := ParseManualProxyString(tt.in); got != tt.want {
			t.Fatalf("ParseManualProxyString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

type fakeResolverBackend struct {
	resolveCalls int
	closeCalls   int
	result       string
	err          error
}

func (f *fakeResolverBackend) resolve(ctx context.Context, rawurl string, cfg Config) (string, error) {
	f.resolveCalls++
	return f.result, f.err
}

func (f *fakeResolverBackend) close() error {
	f.closeCalls++
	return nil
}

func TestTCWINPACREG001ResolverReusesBackendUntilClose(t *testing.T) {
	backend := &fakeResolverBackend{result: "proxy.example.com:8080"}
	resolver := newResolverWithBackend(backend)
	cfg := Config{AutoDetect: true}

	for _, rawurl := range []string{"https://one.example", "https://two.example"} {
		got, err := resolver.ResolveProxyForURL(rawurl, cfg)
		if err != nil {
			t.Fatalf("ResolveProxyForURL(%q) error = %v", rawurl, err)
		}
		if got != backend.result {
			t.Fatalf("ResolveProxyForURL(%q) = %q, want %q", rawurl, got, backend.result)
		}
	}
	if backend.resolveCalls != 2 {
		t.Fatalf("resolve calls = %d, want 2", backend.resolveCalls)
	}
	if backend.closeCalls != 0 {
		t.Fatalf("close calls before Close = %d, want 0", backend.closeCalls)
	}
	if err := resolver.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := resolver.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if backend.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", backend.closeCalls)
	}
	if _, err := resolver.ResolveProxyForURL("https://after-close.example", cfg); !errors.Is(err, ErrResolverClosed) {
		t.Fatalf("ResolveProxyForURL after Close error = %v, want ErrResolverClosed", err)
	}
}

func TestTCWINPACREG003DiscoveryPreservesAvailableSources(t *testing.T) {
	cfg := configFromDiscoveredSources(
		true,
		"http://wpad.example/proxy.pac",
		"http=manual.example:8080",
		"<local>",
	)

	if !cfg.Found || !cfg.AutoDetect || !cfg.IsPAC {
		t.Fatalf("discovered config = %+v, want Found+AutoDetect+IsPAC", cfg)
	}
	if cfg.PACURL != "http://wpad.example/proxy.pac" {
		t.Fatalf("PACURL = %q", cfg.PACURL)
	}
	if cfg.ManualProxy != "manual.example:8080" {
		t.Fatalf("ManualProxy = %q", cfg.ManualProxy)
	}
	if cfg.Bypass != "<local>" {
		t.Fatalf("Bypass = %q", cfg.Bypass)
	}
}

type blockingResolverBackend struct{}

func (blockingResolverBackend) resolve(ctx context.Context, rawurl string, cfg Config) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (blockingResolverBackend) close() error { return nil }

func TestTCWINPACNEG013ResolverBoundsBackendCall(t *testing.T) {
	resolver := newResolverWithBackendTimeout(blockingResolverBackend{}, 25*time.Millisecond)
	defer resolver.Close()

	start := time.Now()
	got, err := resolver.ResolveProxyForURL("https://example.com", Config{AutoDetect: true})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveProxyForURL error = %v, want context deadline exceeded", err)
	}
	if got != "" {
		t.Fatalf("ResolveProxyForURL result = %q, want empty on timeout", got)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("ResolveProxyForURL elapsed = %v, want bounded completion", elapsed)
	}
}

func TestTCWINPACNEG002ResolverPropagatesBackendFailure(t *testing.T) {
	wantErr := errors.New("autodetection failed")
	backend := &fakeResolverBackend{err: wantErr}
	resolver := newResolverWithBackend(backend)
	defer resolver.Close()

	got, err := resolver.ResolveProxyForURL("https://example.com", Config{AutoDetect: true})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolveProxyForURL error = %v, want %v", err, wantErr)
	}
	if got != "" {
		t.Fatalf("ResolveProxyForURL result = %q, want empty on failure", got)
	}
}
