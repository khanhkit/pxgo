package winstartup

import "testing"

func TestBuildRunCommandUsesReleasedExecutable(t *testing.T) {
	exe := `C:\Program Files\Pxgo\pxgo.exe`
	cfg := `C:\Users\Test\pxgo.ini`
	got, err := BuildRunCommand(exe, cfg, func(path string) bool {
		return path == exe || path == cfg
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Program Files\Pxgo\pxgo.exe" "--config=C:\Users\Test\pxgo.ini"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildRunCommandQuotesConfigPath(t *testing.T) {
	exe := `C:\Pxgo\pxgo.exe`
	cfg := `C:\Users\Test User\pxgo.ini`
	got, err := BuildRunCommand(exe, cfg, func(path string) bool {
		return path == exe || path == cfg
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Pxgo\pxgo.exe" "--config=C:\Users\Test User\pxgo.ini"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildRunCommandMissingExecutable(t *testing.T) {
	exe := `C:\Pxgo\pxgo.exe`
	cfg := `C:\pxgo.ini`
	if _, err := BuildRunCommand(exe, cfg, func(path string) bool { return path == cfg }); err == nil {
		t.Fatal("expected missing executable error")
	}
}

func TestBuildRunCommandNonStandardExecutable(t *testing.T) {
	exe := `C:\Tools\custom.exe`
	cfg := `C:\pxgo.ini`
	got, err := BuildRunCommand(exe, cfg, func(path string) bool {
		return path == exe || path == cfg
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Tools\custom.exe" "--config=C:\pxgo.ini"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
