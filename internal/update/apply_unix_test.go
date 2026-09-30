//go:build !windows

package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCandidateAtomicSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pxgo")
	candidate := filepath.Join(dir, "candidate")
	writeExecutable(t, target, "1.0.0")
	writeExecutable(t, candidate, "1.1.0")

	result, err := ApplyCandidate(context.Background(), candidate, target, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Deferred {
		t.Fatal("unix direct apply unexpectedly deferred")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "1.1.0") {
		t.Fatalf("target was not replaced: %q", data)
	}
	if _, err := os.Stat(target + ".pxgo-update-backup"); !os.IsNotExist(err) {
		t.Fatalf("backup retained after success: %v", err)
	}
}

func TestApplyCandidateRollsBackFailedHealthCheck(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pxgo")
	candidate := filepath.Join(dir, "candidate")
	writeExecutable(t, target, "1.0.0")
	writeExecutable(t, candidate, "9.9.9")

	if _, err := ApplyCandidate(context.Background(), candidate, target, "1.1.0"); err == nil {
		t.Fatal("expected candidate health-check failure")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "1.0.0") {
		t.Fatalf("original executable not restored: %q", data)
	}
	if _, err := os.Stat(target + ".pxgo-update-backup"); !os.IsNotExist(err) {
		t.Fatalf("backup retained after rollback: %v", err)
	}
}

func TestApplyCandidateRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	realTarget := filepath.Join(dir, "real-pxgo")
	target := filepath.Join(dir, "pxgo")
	candidate := filepath.Join(dir, "candidate")
	writeExecutable(t, realTarget, "1.0.0")
	writeExecutable(t, candidate, "1.1.0")
	if err := os.Symlink(realTarget, target); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCandidate(context.Background(), candidate, target, "1.1.0"); err == nil {
		t.Fatal("accepted symlink update target")
	}
}

func writeExecutable(t *testing.T, path, version string) {
	t.Helper()
	content := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo " + version + "; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
