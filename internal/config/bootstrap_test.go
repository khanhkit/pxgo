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
		"allow = 127.0.0.0/8,::1",
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

func TestBootstrapLoopbackOnlyByDefault(t *testing.T) {
	if got := Default().Allow; got != "127.0.0.0/8,::1" {
		t.Fatalf("default allow=%q want loopback-only", got)
	}
	dir := t.TempDir()
	setBootstrapConfigDir(t, dir)
	path, _, err := EnsureCanonicalINI("", SystemProxyImport{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "allow = 127.0.0.0/8,::1") {
		t.Fatalf("new config is open to remote clients: %s", raw)
	}
}

func TestApplySystemRoutingWritesOnlyInsideProxySection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	original := "# user comment\n[proxy]\nserver = initial.example:8080\n[settings]\nworkers = 4\n" +
		"server = override.example:8080\nnoproxy = stale.local\n[client]\nclient_auth = NONE\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	system := SystemProxyImport{
		Found:   true,
		PAC:     "http://sys.example/proxy.pac",
		Server:  "unused.example:8080",
		NoProxy: "localhost;*.intranet",
	}
	if _, err := ApplySystemProxyINI(path, system); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	proxySection := strings.SplitN(strings.SplitN(text, "[proxy]\n", 2)[1], "[settings]", 2)[0]
	if !strings.Contains(proxySection, "pac = http://sys.example/proxy.pac") ||
		!strings.Contains(proxySection, "noproxy = localhost;*.intranet") {
		t.Fatalf("proxy routing escaped [proxy] section:\n%s", text)
	}
	settingsSection := strings.SplitN(strings.SplitN(text, "[settings]\n", 2)[1], "[client]", 2)[0]
	if strings.Contains(settingsSection, "\nserver =") || strings.Contains(settingsSection, "\nnoproxy =") {
		t.Fatalf("duplicate routing in [settings] remains active:\n%s", text)
	}
	if !strings.Contains(text, "workers = 4") || !strings.Contains(text, "client_auth = NONE") {
		t.Fatalf("unrelated user settings lost:\n%s", text)
	}
	cfg, err := ReadINI(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PAC != "http://sys.example/proxy.pac" || cfg.Server != "" || cfg.NoProxy != "localhost;*.intranet" {
		t.Fatalf("parser sees stale routing: PAC=%q Server=%q NoProxy=%q", cfg.PAC, cfg.Server, cfg.NoProxy)
	}
	backups, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup not preserved: %v %v", backups, err)
	}
	originalBackup, err := os.ReadFile(backups[0])
	if err != nil || string(originalBackup) != original {
		t.Fatalf("original config backup differs: %v", err)
	}

	// Reapplying the same policy must not create another active routing key.
	if _, err := ApplySystemProxyINI(path, system); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != text {
		t.Fatalf("system routing patch is not idempotent:\nfirst=%s\nsecond=%s", text, second)
	}
}

func TestPatchSystemRoutingCreatesProxySectionWhenAbsent(t *testing.T) {
	original := "[settings]\nworkers = 7\n[client]\nclient_auth = NONE\n"
	got := patchSystemRouting(original, SystemProxyImport{Found: true, Server: "proxy.example:3128"})
	if !strings.Contains(got, "\n[proxy]\n") || !strings.Contains(got, "server = proxy.example:3128") {
		t.Fatalf("missing [proxy] section:\n%s", got)
	}
	if strings.Index(got, "server = proxy.example:3128") < strings.Index(got, "[proxy]") {
		t.Fatalf("routing appeared before [proxy]:\n%s", got)
	}
}

func TestPatchSystemRoutingReusesNormalizedProxySection(t *testing.T) {
	for _, header := range []string{"[ Proxy ]", "[PROXY]", "[ pRoXy ]"} {
		t.Run(header, func(t *testing.T) {
			original := header + "\nserver = stale.example:8080\n[settings]\nworkers = 2\n"
			system := SystemProxyImport{
				Found:   true,
				PAC:     "http://system.example/proxy.pac",
				NoProxy: "localhost;*.intranet",
			}
			got := patchSystemRouting(original, system)
			sectionCount := 0
			for _, line := range strings.Split(got, "\n") {
				if name, ok := normalizedINISectionName(line); ok && name == sectionProxy {
					sectionCount++
				}
			}
			if sectionCount != 1 {
				t.Fatalf("created duplicate [proxy] section for %q:\n%s", header, got)
			}
			proxyBody := strings.SplitN(got, "[settings]", 2)[0]
			if !strings.Contains(proxyBody, "pac = http://system.example/proxy.pac") ||
				!strings.Contains(proxyBody, "noproxy = localhost;*.intranet") {
				t.Fatalf("missing active proxy keys inside %q:\n%s", header, got)
			}
			if second := patchSystemRouting(got, system); second != got {
				t.Fatalf("second apply modified normalized section %q:\nfirst=%s\nsecond=%s", header, got, second)
			}
		})
	}
}
