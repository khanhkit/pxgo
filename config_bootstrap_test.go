package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/systemproxy"
)

func TestSystemProxyImportPrefersPACOverManualHTTPProxy(t *testing.T) {
	got := systemProxyImportFromDiscovered(systemproxy.Config{
		Found:  true,
		IsPAC:  true,
		PACURL: "http://corp/proxy.pac",
		ManualProxy: systemproxy.ManualProxyMap{
			Default: "proxy.corp:8080",
		},
		Bypass: "localhost;*.corp",
	})
	if got.Dynamic || got.PAC != "http://corp/proxy.pac" || got.Server != "proxy.corp:8080" || got.NoProxy != "localhost;*.corp" {
		t.Fatalf("unexpected import: %+v", got)
	}
}

func TestSystemProxyImportPACStillWinsOverPerSchemeManualProxy(t *testing.T) {
	got := systemProxyImportFromDiscovered(systemproxy.Config{
		Found:  true,
		IsPAC:  true,
		PACURL: "http://corp/proxy.pac",
		ManualProxy: systemproxy.ManualProxyMap{ByScheme: map[string]string{
			"http": "a:80", "https": "b:443",
		}},
	})
	if got.Dynamic || got.PAC != "http://corp/proxy.pac" {
		t.Fatalf("fixed PAC must outrank per-scheme manual proxy: %+v", got)
	}
}

func TestSystemProxyImportKeepsAutoDetectAndPerSchemeRoutingDynamic(t *testing.T) {
	for _, tc := range []systemproxy.Config{
		{Found: true, AutoDetect: true, PACURL: "http://corp/proxy.pac", IsPAC: true},
		{Found: true, ManualProxy: systemproxy.ManualProxyMap{ByScheme: map[string]string{"http": "a:80", "https": "b:443"}}},
	} {
		got := systemProxyImportFromDiscovered(tc)
		if !got.Dynamic {
			t.Fatalf("must stay dynamic: input=%+v got=%+v", tc, got)
		}
	}
}

func TestConfigBootstrapE2EFirstRunThenStableSecondRun(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	setMainTestConfigDir(t, filepath.Join(dir, "config"))

	oldDiscover := discoverSystemProxyForConfig
	discoverSystemProxyForConfig = func() systemproxy.Config {
		return systemproxy.Config{
			Supported: true,
			Found:     true,
			IsPAC:     true,
			PACURL:    "http://corp.example/proxy.pac",
			ManualProxy: systemproxy.ManualProxyMap{
				Default: "manual.example:8080",
			},
		}
	}
	t.Cleanup(func() { discoverSystemProxyForConfig = oldDiscover })

	cfg, err := config.ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	path, changed, err := ensureUserConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first run did not generate config")
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "pac = http://corp.example/proxy.pac") || strings.Contains(string(first), "\nserver = manual.example:8080") {
		t.Fatalf("generated routing precedence wrong:\n%s", first)
	}

	cfg, err = config.ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigPath != path || cfg.PAC != "http://corp.example/proxy.pac" || cfg.Listen != "0.0.0.0" || cfg.AutoUpdate != "install" {
		t.Fatalf("second run did not consume generated config: %+v path=%q", cfg, path)
	}
	_, changed, err = ensureUserConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second run rewrote current-schema config")
	}
	second, _ := os.ReadFile(path)
	if string(second) != string(first) {
		t.Fatal("second run changed generated config bytes")
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) != 0 {
		t.Fatalf("idempotent second run created backup: %v", backups)
	}
}

func setMainTestConfigDir(t *testing.T, root string) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", root)
	case "darwin":
		t.Setenv("HOME", root)
	default:
		t.Setenv("XDG_CONFIG_HOME", root)
	}
}

func TestApplySystemProxyCLIEndToEndBacksUpAndPatchesRouting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	original := []byte("# pxgo-config-schema: 1\n[proxy]\nserver = old.proxy:8080\n[settings]\nworkers = 5\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDiscover := discoverSystemProxyForConfig
	discoverSystemProxyForConfig = func() systemproxy.Config {
		return systemproxy.Config{
			Supported: true,
			Found:     true,
			IsPAC:     true,
			PACURL:    "http://corp.example/proxy.pac",
			ManualProxy: systemproxy.ManualProxyMap{
				Default: "manual.example:8080",
			},
			Bypass: "localhost;*.corp.example",
		}
	}
	t.Cleanup(func() { discoverSystemProxyForConfig = oldDiscover })

	if code := runWithMutedIO(t, "--apply-system-proxy", "--config="+path); code != 0 {
		t.Fatalf("apply-system-proxy exit=%d", code)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, want := range []string{
		"pac = http://corp.example/proxy.pac",
		"noproxy = localhost;*.corp.example",
		"workers = 5",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("updated config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\nserver = manual.example:8080") || strings.Contains(text, "\nserver = old.proxy:8080") {
		t.Fatalf("PAC did not replace manual routing:\n%s", text)
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) != 1 {
		t.Fatalf("backups=%v want one", backups)
	}
	backup, _ := os.ReadFile(backups[0])
	if string(backup) != string(original) {
		t.Fatalf("backup mismatch:\n%s", backup)
	}
}
