package winstartup

import (
	"strings"
	"testing"
)

func TestAPISS0033BackgroundExecutableIsSibling(t *testing.T) {
	got, err := BackgroundExecutable(`C:\Program Files\PxGo\pxgo.exe`)
	if err != nil {
		t.Fatal(err)
	}
	want := `C:\Program Files\PxGo\pxgow.exe`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestAPISS0033BackgroundExecutableIsIdempotent(t *testing.T) {
	got, err := BackgroundExecutable(`C:\Program Files\PxGo\pxgow.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(got, `C:\Program Files\PxGo\pxgow.exe`) {
		t.Fatalf("got %q", got)
	}
}

func TestAPISS0033PrepareBackgroundRunCommandUsesWindowlessCompanion(t *testing.T) {
	console := `C:\Program Files\PxGo\pxgo.exe`
	cfg := `C:\Users\Test\pxgo.ini`
	background, err := BackgroundExecutable(console)
	if err != nil {
		t.Fatal(err)
	}
	persisted := false
	got, err := PrepareBackgroundRunCommand(console, cfg, func(path string) bool {
		if path == cfg {
			return persisted
		}
		return path == background
	}, func(path string) error {
		if path != cfg {
			t.Fatalf("save path=%q want=%q", path, cfg)
		}
		persisted = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(got), "pxgow.exe") {
		t.Fatalf("startup command=%q does not use pxgow.exe", got)
	}
	if !strings.Contains(got, "--config="+cfg) {
		t.Fatalf("startup command=%q missing config", got)
	}
}
