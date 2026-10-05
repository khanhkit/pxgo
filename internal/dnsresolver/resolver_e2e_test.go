package dnsresolver

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestDoHE2ECloudflare(t *testing.T) {
	if os.Getenv("CI_DOH_E2E") != "1" {
		t.Skip("set CI_DOH_E2E=1 to run live DoH E2E")
	}
	p, err := New("https://1.1.1.1/dns-query", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, ttl, err := p.LookupIPTTL(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) == 0 {
		t.Fatal("live DoH returned no addresses")
	}
	if ttl <= 0 {
		t.Fatalf("live DoH TTL=%v want > 0", ttl)
	}
}
