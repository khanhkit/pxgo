package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDNSPolicyINIKeepsOrderedRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	content := `[proxy]
DNS = https://1.1.1.1/dns-query
DNS_Only = *.google.com
DNS = udp://9.9.9.9:53
DNS_Bypass = *.bosch.com,bosch.com
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadINI(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DNSRules) != 2 {
		t.Fatalf("DNS rules=%d want 2: %+v", len(cfg.DNSRules), cfg.DNSRules)
	}
	if cfg.DNSRules[0].Resolver != "https://1.1.1.1/dns-query" || len(cfg.DNSRules[0].Only) != 1 || cfg.DNSRules[0].Only[0] != "*.google.com" {
		t.Fatalf("first DNS rule=%+v", cfg.DNSRules[0])
	}
	if cfg.DNSRules[1].Resolver != "udp://9.9.9.9:53" || len(cfg.DNSRules[1].Bypass) != 2 {
		t.Fatalf("second DNS rule=%+v", cfg.DNSRules[1])
	}
}

func TestDNSPolicyINIRejectsSelectorBeforeDNS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	if err := os.WriteFile(path, []byte("[proxy]\nDNS_Only = *.google.com\nDNS = https://1.1.1.1/dns-query\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadINI(path); err == nil {
		t.Fatal("expected DNS_Only before DNS to fail")
	}
}

func TestDNSPolicyEnvironmentOverridesINIRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	content := "[proxy]\ndns = udp://1.1.1.1:53\ndns_only = old.example\ndns = udp://9.9.9.9:53\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PXGO_DNS", "https://1.1.1.1/dns-query")
	t.Setenv("PXGO_DNS_ONLY", "env.example")
	cfg, err := ParseArgs([]string{"--config=" + path})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DNSRules) != 1 || cfg.DNSRules[0].Resolver != "https://1.1.1.1/dns-query" {
		t.Fatalf("environment DNS rules=%+v", cfg.DNSRules)
	}
	if len(cfg.DNSRules[0].Only) != 1 || cfg.DNSRules[0].Only[0] != "env.example" {
		t.Fatalf("environment DNS_Only=%+v", cfg.DNSRules[0].Only)
	}
}

func TestDNSPolicyCLISelectorsApplyToCLIRule(t *testing.T) {
	cfg, err := ParseArgs([]string{
		"--dns=https://1.1.1.1/dns-query",
		"--dns-only=*.google.com",
		"--dns-bypass=*.corp.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DNSRules) != 1 {
		t.Fatalf("CLI DNS rules=%+v", cfg.DNSRules)
	}
	rule := cfg.DNSRules[0]
	if len(rule.Only) != 1 || rule.Only[0] != "*.google.com" || len(rule.Bypass) != 1 || rule.Bypass[0] != "*.corp.example" {
		t.Fatalf("CLI DNS rule=%+v", rule)
	}
}

func TestDNSPolicySaveINIRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.DNSRules = []DNSRule{
		{Resolver: "https://1.1.1.1/dns-query", Only: []string{"*.google.com"}},
		{Resolver: "udp://9.9.9.9:53", Bypass: []string{"*.bosch.com"}},
	}
	cfg.DNS = cfg.DNSRules[len(cfg.DNSRules)-1].Resolver
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	if err := SaveINI(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := ReadINI(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DNSRules) != 2 || got.DNSRules[0].Resolver != cfg.DNSRules[0].Resolver || got.DNSRules[1].Resolver != cfg.DNSRules[1].Resolver {
		t.Fatalf("round-trip DNS rules=%+v", got.DNSRules)
	}
	if len(got.DNSRules[0].Only) != 1 || got.DNSRules[0].Only[0] != "*.google.com" {
		t.Fatalf("round-trip DNS_Only=%+v", got.DNSRules[0].Only)
	}
	if len(got.DNSRules[1].Bypass) != 1 || got.DNSRules[1].Bypass[0] != "*.bosch.com" {
		t.Fatalf("round-trip DNS_Bypass=%+v", got.DNSRules[1].Bypass)
	}
}
