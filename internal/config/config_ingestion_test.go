package config

import (
	"os"
	"path/filepath"
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
