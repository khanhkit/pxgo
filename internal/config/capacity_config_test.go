package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaxConnectionsConfigAndSaveRoundTrip(t *testing.T) {
	cfg := Default()
	if cfg.MaxConnections != 0 || Defaults[keyMaxConnections] != "0" {
		t.Fatalf("default %+v", cfg)
	}
	for _, x := range []string{"-1", "-123456789999999999999999", "nan", "", "3.14", "-0x1"} {
		copy := Default()
		if err := applyValueFrom(&copy, "max-connections", x, "cli"); err == nil {
			t.Fatalf("accepted %q", x)
		}
	}
	if err := applyValueFrom(&cfg, "max-connections", "99", "cli"); err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConnections != 99 || cfg.SourceOf("max-connections") != "cli" {
		t.Fatalf("applied %+v", cfg)
	}
	p := filepath.Join(t.TempDir(), "pxgo.ini")
	if err := SaveINI(p, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"max-connections = 99", "NOT recommended", "HARD CEILING"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s from save", want)
		}
	}
	read, err := ReadINI(p)
	if err != nil {
		t.Fatal(err)
	}
	if read.MaxConnections != 99 {
		t.Fatalf("round trip %+v", read)
	}
}

func TestMaxConnectionsEnvAndCLIPrecedence(t *testing.T) {
	t.Setenv("PX_MAX_CONNECTIONS", "7")
	t.Setenv("PXGO_MAX_CONNECTIONS", "10")
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "shadow")
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "settings.ini")
	if err := os.WriteFile(cfgFile, []byte("[settings]\nmax-connections=2\nworkers=1\nthreads=32\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseArgs([]string{"--config=" + cfgFile})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConnections != 10 || cfg.SourceOf("max-connections") != "env:PXGO_MAX_CONNECTIONS" {
		t.Fatalf("env precedence %+v", cfg)
	}
	cfg, err = ParseArgs([]string{"--config=" + cfgFile, "--max-connections=12"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConnections != 12 || cfg.SourceOf("max-connections") != "cli" {
		t.Fatalf("CLI precedence %+v", cfg)
	}
	if _, err := ParseArgs([]string{"--config=" + cfgFile, "--max-connections"}); err == nil {
		t.Fatal("bare CLI setting must be rejected")
	}
}

func TestExperimentalClassAndResearchPolicyEnvAreAuxiliary(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_RESEARCH_CAPACITY_POLICY", "fast_saturation")
	cfg, err := ParseArgs(nil)
	if err != nil {
		t.Fatalf("documented internal preview-only environment keys must not break parsing: %v", err)
	}
	if cfg.MaxConnections != 0 {
		t.Fatalf("research environment changed public config: %+v", cfg)
	}
}
