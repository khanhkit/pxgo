//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackgroundLogPathIsPxGoLocal(t *testing.T) {
	path, err := backgroundLogPath()
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := filepath.Join("pxgo", "background-startup.log")
	if !strings.HasSuffix(strings.ToLower(path), strings.ToLower(wantSuffix)) {
		t.Fatalf("background log path=%q want suffix %q", path, wantSuffix)
	}
}

func TestBackgroundLogWriterCapturesHiddenRuntimeDiagnostic(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	f, err := openBackgroundLog()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("hidden startup failure\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pxgo", "background-startup.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hidden startup failure") {
		t.Fatalf("background log missing diagnostic: %q", data)
	}
}
