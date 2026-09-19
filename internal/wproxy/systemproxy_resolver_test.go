package wproxy

import (
	"errors"
	"strings"
	"testing"

	"github.com/pavelsimo/pxgo/internal/systemproxy"
)

type fakeSystemResolver struct {
	result string
	err    error
	calls  int
	closes int
}

func (f *fakeSystemResolver) ResolveProxyForURL(rawurl string, cfg systemproxy.Config) (string, error) {
	f.calls++
	return f.result, f.err
}

func (f *fakeSystemResolver) Close() error {
	f.closes++
	return nil
}

func TestTCWINPACNEG004WproxyPropagatesSystemResolverFailure(t *testing.T) {
	wantErr := errors.New("WinHTTP autodetection failed")
	resolver := &fakeSystemResolver{err: wantErr}
	w := &Wproxy{Mode: ModeAuto, systemResolver: resolver}

	servers, _, _, err := w.FindProxyForURL("https://example.com")
	if !errors.Is(err, wantErr) {
		t.Fatalf("FindProxyForURL error = %v, want %v", err, wantErr)
	}
	if servers != nil {
		t.Fatalf("FindProxyForURL servers = %#v, want nil on resolver failure", servers)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
}

func TestTCWINPACREG005WproxyOwnsResolverLifecycle(t *testing.T) {
	resolver := &fakeSystemResolver{result: "DIRECT"}
	w := &Wproxy{Mode: ModeAuto, systemResolver: resolver}

	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if resolver.closes != 1 {
		t.Fatalf("resolver close calls = %d, want 1", resolver.closes)
	}

	_, _, _, err := w.FindProxyForURL("https://example.com")
	if err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("FindProxyForURL after Close error = %v, want resolver lifecycle error", err)
	}
}
