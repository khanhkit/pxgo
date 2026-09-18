package config

import (
	"path/filepath"
	"testing"
)

func TestAPISS0011InstallAllowsMissingExplicitConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh", "pxgo.ini")
	cfg, err := ParseArgs([]string{
		"--install",
		"--config=" + path,
		"--server=proxy.example.test:8080",
		"--port=4141",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Install {
		t.Fatal("install action was not preserved")
	}
	if cfg.ConfigPath != path {
		t.Fatalf("config path=%q want=%q", cfg.ConfigPath, path)
	}
	if cfg.Server != "proxy.example.test:8080" || cfg.Port != 4141 {
		t.Fatalf("effective config lost: %#v", cfg)
	}
}
