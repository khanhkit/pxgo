package winstartup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPISS0011UsesReleasedExecutableDirectly(t *testing.T) {
	exe := `C:\Program Files\PxGo\pxgo.exe`
	cfg := `C:\Users\Test\pxgo.ini`
	exists := func(path string) bool {
		return path == exe || path == cfg
	}

	got, err := BuildRunCommand(exe, cfg, exists)
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Program Files\PxGo\pxgo.exe" "--config=C:\Users\Test\pxgo.ini"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if strings.Contains(strings.ToLower(got), "pxgow.exe") {
		t.Fatalf("startup command references unreleased pxgow.exe: %q", got)
	}
}

func TestAPISS0011RequiresExistenceChecker(t *testing.T) {
	if _, err := BuildRunCommand(`C:\PxGo\pxgo.exe`, `C:\Cfg\pxgo.ini`, nil); err == nil {
		t.Fatal("expected nil existence checker to fail")
	}
}

func TestAPISS0011RequiresExistingExecutableAndConfig(t *testing.T) {
	exe := `C:\PxGo\pxgo.exe`
	cfg := `C:\Cfg\pxgo.ini`

	if _, err := BuildRunCommand(exe, cfg, func(path string) bool { return path == cfg }); err == nil {
		t.Fatal("expected missing executable to fail")
	}
	if _, err := BuildRunCommand(exe, cfg, func(path string) bool { return path == exe }); err == nil {
		t.Fatal("expected missing config to fail")
	}
}

func TestAPISS0011AlwaysQuotesWindowsArguments(t *testing.T) {
	exe := `C:\Tools&Stuff\pxgo.exe`
	cfg := `C:\Cfg^&%TEMP%\pxgo.ini`
	exists := func(path string) bool { return path == exe || path == cfg }

	got, err := BuildRunCommand(exe, cfg, exists)
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Tools&Stuff\pxgo.exe" "--config=C:\Cfg^&%TEMP%\pxgo.ini"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAPISS0011RegistryValueNameIsPxGoSpecific(t *testing.T) {
	if RegistryValueName != "PxGo" {
		t.Fatalf("registry value=%q want PxGo", RegistryValueName)
	}
}

func TestAPISS0011ReleaseBuildsWindowsPxgoArtifact(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "project_name: pxgo") || !strings.Contains(text, "- windows") {
		t.Fatal("GoReleaser config does not establish a Windows pxgo artifact")
	}
}

func TestAPISS0011PrepareRunCommandPersistsConfigFirst(t *testing.T) {
	exe := `C:\PxGo\pxgo.exe`
	cfg := `C:\Fresh Config\pxgo.ini`
	configExists := false
	var events []string

	exists := func(path string) bool {
		switch path {
		case exe:
			return true
		case cfg:
			events = append(events, "exists-config")
			return configExists
		default:
			return false
		}
	}
	save := func(path string) error {
		events = append(events, "save-config")
		if path != cfg {
			t.Fatalf("save path=%q want=%q", path, cfg)
		}
		configExists = true
		return nil
	}

	got, err := PrepareRunCommand(exe, cfg, exists, save)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0] != "save-config" || events[1] != "exists-config" {
		t.Fatalf("event order=%v, want save before existence validation", events)
	}
	want := `"C:\PxGo\pxgo.exe" "--config=C:\Fresh Config\pxgo.ini"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAPISS0011PrepareRunCommandStopsOnSaveFailure(t *testing.T) {
	wantErr := errors.New("save failed")
	_, err := PrepareRunCommand(
		`C:\PxGo\pxgo.exe`,
		`C:\Cfg\pxgo.ini`,
		func(string) bool { t.Fatal("existence check must not run after save failure"); return false },
		func(string) error { return wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v want %v", err, wantErr)
	}
}
