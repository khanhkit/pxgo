package proxy

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestDoHE2EProxyHTTPAndCONNECT(t *testing.T) {
	if os.Getenv("CI_DOH_E2E") != "1" {
		t.Skip("set CI_DOH_E2E=1 to run live DoH E2E")
	}
	cfg := config.Default()
	cfg.Server = "DIRECT"
	cfg.DNS = "https://1.1.1.1/dns-query"
	cfg.DNSRules = []config.DNSRule{{Resolver: cfg.DNS, Only: []string{"example.com"}}}
	cfg.SockTimeout = 8
	px := startTestProxy(t, cfg)
	client := proxyClient(t, px.Port())
	client.Timeout = 15 * time.Second

	for _, tc := range []struct {
		name   string
		target string
	}{{"HTTP", "http://example.com/"}, {"CONNECT", "https://example.com/"}} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Get(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 400 {
				t.Fatalf("status=%s body=%q", resp.Status, body)
			}
		})
	}
}
