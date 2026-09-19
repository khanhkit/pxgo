package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAPISS0009ReadINIRejectsInvalidNumeric(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	if err := os.WriteFile(path, []byte("[proxy]\nport = not-a-port\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadINI(path); err == nil {
		t.Fatal("expected invalid numeric INI value to fail")
	}
}

func TestAPISS0009ReadINIRejectsUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	if err := os.WriteFile(path, []byte("[proxy]\ndefinitely_unknown = value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadINI(path); err == nil {
		t.Fatal("expected unknown INI key to fail")
	}
}

func TestAPISS0009EnvironmentInvalidNumericFails(t *testing.T) {
	t.Setenv("PXGO_PORT", "not-a-port")
	if _, err := ParseArgs(nil); err == nil {
		t.Fatal("expected invalid PXGO_PORT to fail")
	}
}

func TestAPISS0009EmptyEnvironmentOverridesLowerPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	if err := os.WriteFile(path, []byte("[proxy]\nusername = from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PXGO_USERNAME", "")
	cfg, err := ParseArgs([]string{"--config=" + path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != "" {
		t.Fatalf("empty environment override lost; username=%q", cfg.Username)
	}
}

func TestAPISS0009ExplicitMissingPACFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.pac")
	if _, err := ParseArgs([]string{"--pac=" + missing}); err == nil {
		t.Fatal("expected explicit missing PAC path to fail")
	}
}

func TestAPISS0009DoesNotTrustCWDDotenvByDefault(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env", []byte("PXGO_USERNAME=cwd-injected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PXGO_USERNAME", "")
	cfg, err := ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username == "cwd-injected" {
		t.Fatal("implicit CWD .env injected configuration")
	}
}

func TestAPISS0009RecordsEffectiveSourceProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	if err := os.WriteFile(path, []byte("[proxy]\nserver = file.proxy:8080\nport = 1111\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PXGO_THREADS", "8")
	cfg, err := ParseArgs([]string{"--config=" + path, "--port=3333"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SourceOf("server"); got != "ini:"+path {
		t.Fatalf("server source=%q", got)
	}
	if got := cfg.SourceOf("threads"); got != "env:PXGO_THREADS" {
		t.Fatalf("threads source=%q", got)
	}
	if got := cfg.SourceOf("port"); got != "cli" {
		t.Fatalf("port source=%q", got)
	}
	if got := cfg.SourceOf("idle"); got != "default" {
		t.Fatalf("idle source=%q", got)
	}
}

func TestAPISS0009ReadINIAcceptsSemicolonComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	if err := os.WriteFile(path, []byte("[proxy]\n; documented comment\nport = 4141\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadINI(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 4141 {
		t.Fatalf("port=%d", cfg.Port)
	}
}

func TestAPISS0009ReadINIAcceptsBoundedLongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	value := strings.Repeat("x", 70<<10)
	if err := os.WriteFile(path, []byte("[proxy]\nusername = "+value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadINI(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != value {
		t.Fatalf("username length=%d want=%d", len(cfg.Username), len(value))
	}
}

func TestAPISS0009ReadINIRejectsOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	value := strings.Repeat("x", maxConfigLineBytes+1)
	if err := os.WriteFile(path, []byte("username = "+value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadINI(path); err == nil {
		t.Fatal("expected oversized INI line to fail")
	}
}

func TestAPISS0009MalformedExplicitDotenvFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("PXGO_PORT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PXGO_DOTENV", path)
	if _, err := ParseArgs(nil); err == nil {
		t.Fatal("expected malformed explicit dotenv to fail")
	}
}

func TestAPISS0009UnknownEnvironmentKeyFails(t *testing.T) {
	t.Setenv("PXGO_DEFINITELY_UNKNOWN", "1")
	if _, err := ParseArgs(nil); err == nil {
		t.Fatal("expected unknown PXGO environment key to fail")
	}
}

func TestAPISS0009InvalidPACFileURLFails(t *testing.T) {
	if _, err := ParseArgs([]string{"--pac=file:///%zz"}); err == nil {
		t.Fatal("expected invalid PAC file URL to fail")
	}
}

func TestAPISS0009ExplicitConfigDirectoryFails(t *testing.T) {
	if _, err := ParseArgs([]string{"--config=" + t.TempDir()}); err == nil {
		t.Fatal("expected config directory to fail")
	}
}

func TestAPISS0009HomeDirFailurePropagates(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("Windows config-dir resolution does not call os.UserHomeDir")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	oldUserHomeDir := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("home unavailable") }
	t.Cleanup(func() { userHomeDir = oldUserHomeDir })
	if _, err := ParseArgs(nil); err == nil || !strings.Contains(err.Error(), "home unavailable") {
		t.Fatalf("expected home-dir error, got %v", err)
	}
}

func TestAPISS0009FileURLPreservesUNCServerPrefix(t *testing.T) {
	got, err := fileURLToLocalPathStrict("file://server/share/proxy.pac")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.FromSlash("//server/share/proxy.pac")
	if got != want {
		t.Fatalf("UNC path=%q want=%q", got, want)
	}
}
