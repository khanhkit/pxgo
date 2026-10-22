package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// A parent proxy rejecting CONNECT is enough to prove that DoH no longer
// attempts direct 1.1.1.1:443. No network access or TLS bypass is needed.
func TestDoHBootstrapUsesUpstreamCONNECT(t *testing.T) {
	var connects atomic.Int32
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "203.0.113.77:443" {
			t.Errorf("upstream method=%q host=%q", r.Method, r.Host)
		}
		connects.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer parent.Close()
	parentAddr := strings.TrimPrefix(parent.URL, "http://")
	_, parentPort, err := net.SplitHostPort(parentAddr)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"manual", "PAC", "rules"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.DNS = "https://203.0.113.77/dns-query"
			cfg.SockTimeout = 0.2
			if mode == "manual" || mode == "rules" {
				cfg.Server = "localhost:" + parentPort // system DNS resolves proxy hostname
				if mode == "rules" {
					cfg.DNSRules = []config.DNSRule{{Resolver: cfg.DNS, Only: []string{"*.test"}}}
				}
			} else {
				pacPath := filepath.Join(t.TempDir(), "proxy.pac")
				pacContent := fmt.Sprintf(`function FindProxyForURL(url, host) { return "PROXY localhost:%s"; }`, parentPort)
				if err := os.WriteFile(pacPath, []byte(pacContent), 0600); err != nil {
					t.Fatal(err)
				}
				cfg.PAC = pacPath
			}
			px, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer px.Shutdown(context.Background())
			before := connects.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := px.dnsResolver.LookupIP(ctx, "bootstrap-fixture.test"); err == nil {
				t.Fatal("expected parent proxy CONNECT rejection")
			}
			if connects.Load() == before {
				t.Fatalf("DoH did not use parent CONNECT via %s routing", mode)
			}
		})
	}
}

func TestDoHBootstrapRejectsLocalPxGoProxyLoop(t *testing.T) {
	cfg := config.Default()
	cfg.DNS = "https://203.0.113.77/dns-query"
	cfg.Server = fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	cfg.SockTimeout = 0.2
	px, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer px.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := px.dnsResolver.LookupIP(ctx, "self-loop.test"); err == nil {
		t.Fatal("expected fail-closed bootstrap loop rejection")
	}
}
