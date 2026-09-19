//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package kerberos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultKinitPasswordRunnerUsesPTY(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "kinit")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
if [ ! -t 0 ]; then
  echo no-tty >&2
  exit 7
fi
IFS= read -r password
if [ "$password" != "secret" ]; then
  echo bad-password >&2
  exit 8
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := defaultKinitPasswordRunner(5*time.Second, "user@REALM", nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}
	if strings.Contains(result.Stdout, "secret") || strings.Contains(result.Stderr, "secret") {
		t.Fatalf("password leaked through PTY echo: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestDefaultKinitPasswordRunnerTimeoutCleanupIsBounded(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "kinit")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
if [ ! -t 0 ]; then
  exit 7
fi
while IFS= read -r line; do
  :
done
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	started := time.Now()
	result, err := defaultKinitPasswordRunner(100*time.Millisecond, "user@REALM", nil, "secret")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("PTY cleanup took %s, want bounded cleanup", elapsed)
	}
	if strings.Contains(result.Stdout, "secret") || strings.Contains(result.Stderr, "secret") {
		t.Fatalf("password leaked during timeout cleanup: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestDefaultKinitPasswordRunnerBuffersPasswordUntilReaderReady(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "kinit")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
if [ ! -t 0 ]; then
  exit 7
fi
sleep 0.1
IFS= read -r password
if [ "$password" != "secret" ]; then
  echo bad-password >&2
  exit 8
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := defaultKinitPasswordRunner(2*time.Second, "user@REALM", nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}
	if strings.Contains(result.Stdout, "secret") || strings.Contains(result.Stderr, "secret") {
		t.Fatalf("password leaked with delayed reader: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}
