//go:build windows

package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceStateFileOverwritesAndKeepsPreviousOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	source := filepath.Join(dir, "tmp-state.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceStateFile(source, target); err == nil {
		t.Fatal("expected missing source error")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "old" {
		t.Fatalf("previous state lost after failed replace: %q %v", data, err)
	}
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceStateFile(source, target); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "new" {
		t.Fatalf("new state not published: %q %v", data, err)
	}
}
