package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBootstrapDefaultsEnablePortableUpdatesAndListenAllInterfaces(t *testing.T) {
	cfg := Default()
	if cfg.Listen != "0.0.0.0" {
		t.Fatalf("Listen=%q want 0.0.0.0", cfg.Listen)
	}
	if cfg.AutoUpdate != "install" {
		t.Fatalf("AutoUpdate=%q want install", cfg.AutoUpdate)
	}
}

func TestEnsureCanonicalINIFreshDocumentsOptionalFeatures(t *testing.T) {
	dir := t.TempDir()
	setBootstrapConfigDir(t, dir)
	path := filepath.Join(GetConfigDir(), "pxgo.ini")

	got, changed, err := EnsureCanonicalINI("", SystemProxyImport{})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || got != path {
		t.Fatalf("path=%q changed=%v want %q,true", got, changed, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"# pxgo-config-schema: 1",
		"listen = 0.0.0.0",
		"auto_update = install",
		"update_interval = 24h",
		"update_channel = stable",
		"install_provider = auto",
		"# dns = https://1.1.1.1/dns-query",
		"# dns_only = *.google.com",
		"# dns_bypass = bosch.com,*.bosch.com",
		"WARNING: listen=0.0.0.0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated INI missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\ndns = ") {
		t.Fatalf("DNS must stay opt-in/commented:\n%s", text)
	}
}

func TestEnsureCanonicalINIFreshPrefersPACOverManualProxy(t *testing.T) {
	dir := t.TempDir()
	setBootstrapConfigDir(t, dir)

	path, _, err := EnsureCanonicalINI("", SystemProxyImport{
		Found:   true,
		PAC:     "http://corp.example/proxy.pac",
		Server:  "proxy.example:8080",
		NoProxy: "localhost;*.corp.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	if !strings.Contains(text, "pac = http://corp.example/proxy.pac") {
		t.Fatalf("PAC not imported:\n%s", text)
	}
	if strings.Contains(text, "\nserver = proxy.example:8080") {
		t.Fatalf("manual proxy must not win over PAC:\n%s", text)
	}
}

func TestEnsureCanonicalINIMigratesLegacyWithoutTouchingPXIni(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "px.ini")
	legacyRaw := []byte("# keep me\n[proxy]\nserver = legacy.proxy:8080\nport = 4242\n")
	if err := os.WriteFile(legacy, legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	path, changed, err := EnsureCanonicalINI(legacy, SystemProxyImport{Found: true, Server: "system.proxy:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || path != filepath.Join(dir, "pxgo.ini") {
		t.Fatalf("path=%q changed=%v", path, changed)
	}
	gotLegacy, _ := os.ReadFile(legacy)
	if string(gotLegacy) != string(legacyRaw) {
		t.Fatalf("px.ini was modified:\n%s", gotLegacy)
	}
	generated, _ := os.ReadFile(path)
	text := string(generated)
	if !strings.Contains(text, "server = legacy.proxy:8080") || strings.Contains(text, "system.proxy:8080") {
		t.Fatalf("legacy config must outrank system config:\n%s", text)
	}
	if !strings.Contains(text, "auto_update = install") || !strings.Contains(text, "# pxgo-config-schema: 1") {
		t.Fatalf("PxGo settings not appended:\n%s", text)
	}
}

func TestEnsureCanonicalINIUpgradeBacksUpAndPreservesUserValuesAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	original := "# personal comment\n[proxy]\nserver = user.proxy:8080\n[settings]\nauto_update = off\nworkers = 7\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	_, changed, err := EnsureCanonicalINI(path, SystemProxyImport{Found: true, Server: "system.proxy:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected schema upgrade")
	}
	upgraded, _ := os.ReadFile(path)
	text := string(upgraded)
	for _, want := range []string{"# personal comment", "server = user.proxy:8080", "auto_update = off", "workers = 7", "# pxgo-config-schema: 1", "update_interval = 24h"} {
		if !strings.Contains(text, want) {
			t.Fatalf("upgraded config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "system.proxy:8080") || strings.Contains(text, "auto_update = install") {
		t.Fatalf("upgrade overwrote user intent:\n%s", text)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "pxgo.ini.bak.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	backup, _ := os.ReadFile(backups[0])
	if string(backup) != original {
		t.Fatalf("backup mismatch:\n%s", backup)
	}

	before := string(upgraded)
	_, changed, err = EnsureCanonicalINI(path, SystemProxyImport{})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("schema migration must be idempotent")
	}
	after, _ := os.ReadFile(path)
	if string(after) != before {
		t.Fatal("idempotent migration rewrote config")
	}
}

func TestApplySystemProxyINIBacksUpAndPACWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	original := "# personal\n[proxy]\nserver = old.proxy:8080\npac = http://old/p.pac\nnoproxy = old.local\n[settings]\nworkers = 4\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ApplySystemProxyINI(path, SystemProxyImport{
		Found:   true,
		PAC:     "http://system/proxy.pac",
		Server:  "system.proxy:8080",
		NoProxy: "localhost;*.corp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path=%q want %q", got, path)
	}
	updated, _ := os.ReadFile(path)
	text := string(updated)
	if !strings.Contains(text, "pac = http://system/proxy.pac") || strings.Contains(text, "\nserver = system.proxy:8080") {
		t.Fatalf("PAC must win:\n%s", text)
	}
	if !strings.Contains(text, "noproxy = localhost;*.corp") || !strings.Contains(text, "workers = 4") {
		t.Fatalf("system routing patch lost unrelated settings:\n%s", text)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "pxgo.ini.bak.*"))
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %v", backups)
	}
}

func setBootstrapConfigDir(t *testing.T, dir string) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", filepath.Dir(dir))
	case "darwin":
		old := userHomeDir
		userHomeDir = func() (string, error) { return filepath.Dir(dir), nil }
		t.Cleanup(func() { userHomeDir = old })
	default:
		t.Setenv("XDG_CONFIG_HOME", filepath.Dir(dir))
	}
}

func TestSaveINIBacksUpExistingUserConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	original := []byte("# user-owned\n[settings]\nworkers = 9\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Workers = 2
	if err := SaveINI(path, cfg); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) != 1 {
		t.Fatalf("backups=%v want one", backups)
	}
	got, _ := os.ReadFile(backups[0])
	if string(got) != string(original) {
		t.Fatalf("backup=%q want %q", got, original)
	}
}

func TestApplySystemProxyINIDynamicRoutingDoesNotSnapshotBypass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	original := "[proxy]\nserver = old.proxy:8080\nnoproxy = old.local\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ApplySystemProxyINI(path, SystemProxyImport{
		Found:   true,
		Dynamic: true,
		NoProxy: "current-but-dynamic.local",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	if strings.Contains(text, "\nnoproxy = current-but-dynamic.local") {
		t.Fatalf("dynamic system routing must not snapshot bypass:\n%s", text)
	}
	if strings.Contains(text, "\nserver = old.proxy:8080") || strings.Contains(text, "\nnoproxy = old.local") {
		t.Fatalf("explicit routing was not disabled for dynamic system mode:\n%s", text)
	}
}

func TestSystemProxyImportRejectsINIInjectionWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	original := []byte("# pxgo-config-schema: 1\n[proxy]\nserver = safe.proxy:8080\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplySystemProxyINI(path, SystemProxyImport{Found: true, PAC: "http://safe/p.pac\nserver = injected:80"}); err == nil {
		t.Fatal("expected system proxy line injection to be rejected")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("rejected import mutated config:\n%s", got)
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) != 0 {
		t.Fatalf("rejected import created backup: %v", backups)
	}
}

func TestLegacyMigrationFallsBackWhenSiblingDirectoryIsReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions are not reliably modeled by chmod on Windows")
	}
	legacyDir := t.TempDir()
	legacy := filepath.Join(legacyDir, "px.ini")
	legacyRaw := []byte("[proxy]\nserver = legacy.proxy:8080\n")
	if err := os.WriteFile(legacy, legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	fallbackDir := filepath.Join(t.TempDir(), "pxgo")
	setBootstrapConfigDir(t, fallbackDir)
	wantFallback := filepath.Join(GetConfigDir(), "pxgo.ini")
	if err := os.Chmod(legacyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacyDir, 0o700) })

	path, changed, err := EnsureCanonicalINI(legacy, SystemProxyImport{})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || path != wantFallback {
		t.Fatalf("path=%q changed=%v want fallback %q,true", path, changed, wantFallback)
	}
	gotLegacy, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotLegacy) != string(legacyRaw) {
		t.Fatalf("legacy config mutated:\n%s", gotLegacy)
	}
}

func TestSchemaUpgradeBackupFailureLeavesOriginalUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions are not reliably modeled by chmod on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	original := []byte("[proxy]\nserver = user.proxy:8080\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	// Pre-create the lock so the transaction can lock the file after the
	// directory becomes read-only; backup creation is then the failing step.
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, changed, err := EnsureCanonicalINI(path, SystemProxyImport{})
	if err == nil {
		t.Fatal("expected backup creation failure")
	}
	if changed {
		t.Fatal("failed upgrade reported a mutation")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("failed upgrade mutated config:\n%s", got)
	}
}
