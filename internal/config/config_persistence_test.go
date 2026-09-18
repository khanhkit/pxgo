package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSaveINIRejectsLineInjectionAndPreservesLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	const lastGood = "[proxy]\nserver = last-good.example:8080\n"
	if err := os.WriteFile(path, []byte(lastGood), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	cfg.NoProxy = "safe.example\nport = 1"
	if err := SaveINI(path, cfg); err == nil {
		t.Fatal("SaveINI accepted a value containing a newline")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != lastGood {
		t.Fatalf("failed save replaced last-good config:\n%s", got)
	}
}

func TestSaveINIRejectsCarriageReturnInjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pxgo.ini")
	cfg := Default()
	cfg.UserAgent = "safe\r[settings]"
	if err := SaveINI(path, cfg); err == nil {
		t.Fatal("SaveINI accepted a value containing a carriage return")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid config should not create destination, stat err=%v", err)
	}
}

func TestPlaintextKeyringCorruptionIsReportedAndPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	const corrupt = `{"pxgo":{"old":"secret"}`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PXGO_KEYRING_PLAINTEXT", "1")
	t.Setenv("PXGO_KEYRING_FILE", path)

	if err := StorePassword(Realm, "new-user", "new-secret"); err == nil {
		t.Fatal("StorePassword silently replaced corrupt plaintext keyring")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != corrupt {
		t.Fatalf("corrupt last-good evidence was overwritten: %q", got)
	}
}

func TestPlaintextKeyringTightensExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not an ACL assertion on Windows")
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(`{"pxgo":{"old":"secret"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PXGO_KEYRING_PLAINTEXT", "1")
	t.Setenv("PXGO_KEYRING_FILE", path)
	if err := StorePassword(Realm, "new-user", "new-secret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("plaintext keyring mode=%#o want 0600", got)
	}
}

func TestPlaintextKeyringConcurrentProcessesPreserveAllUpdates(t *testing.T) {
	const writers = 24
	dir := t.TempDir()
	keyring := filepath.Join(dir, "keyring.json")
	barrier := filepath.Join(dir, "start")

	type writerProcess struct {
		cmd *exec.Cmd
		out bytes.Buffer
	}
	cmds := make([]*writerProcess, 0, writers)
	for i := 0; i < writers; i++ {
		user := fmt.Sprintf("user-%02d", i)
		pass := fmt.Sprintf("password-%02d", i)
		cmd := exec.Command(os.Args[0], "-test.run=^TestPlaintextKeyringProcessWriter$", "-test.count=1")
		cmd.Env = append(os.Environ(),
			"PXGO_TEST_KEYRING_WRITER="+user+"="+pass,
			"PXGO_TEST_KEYRING_BARRIER="+barrier,
			"PXGO_KEYRING_PLAINTEXT=1",
			"PXGO_KEYRING_FILE="+keyring,
		)
		proc := &writerProcess{cmd: cmd}
		cmd.Stdout = &proc.out
		cmd.Stderr = &proc.out
		cmds = append(cmds, proc)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start writer %d: %v", i, err)
		}
	}
	if err := os.WriteFile(barrier, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, proc := range cmds {
		if err := proc.cmd.Wait(); err != nil {
			t.Fatalf("writer %d failed: %v\n%s", i, err, proc.out.String())
		}
	}

	raw, err := os.ReadFile(keyring)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("final keyring invalid JSON: %v\n%s", err, raw)
	}
	if got := len(data[Realm]); got != writers {
		t.Fatalf("concurrent plaintext writers preserved %d/%d credentials: %s", got, writers, raw)
	}
	for i := 0; i < writers; i++ {
		user := fmt.Sprintf("user-%02d", i)
		pass := fmt.Sprintf("password-%02d", i)
		if got := data[Realm][user]; got != pass {
			t.Fatalf("credential %s=%q want %q", user, got, pass)
		}
	}
}

func TestPlaintextKeyringProcessWriter(t *testing.T) {
	spec := os.Getenv("PXGO_TEST_KEYRING_WRITER")
	if spec == "" {
		return
	}
	user, pass, ok := strings.Cut(spec, "=")
	if !ok || user == "" {
		t.Fatalf("bad writer spec %q", spec)
	}
	barrier := os.Getenv("PXGO_TEST_KEYRING_BARRIER")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for writer barrier")
		}
		time.Sleep(time.Millisecond)
	}
	if err := StorePassword(Realm, user, pass); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicWriteReplaceFailurePreservesLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pxgo.ini")
	const lastGood = "last-good\n"
	if err := os.WriteFile(path, []byte(lastGood), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("injected replace failure")
	err := atomicWriteFileWithReplace(path, []byte("new-partial\n"), 0o600, func(_, _ string) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("error=%v want injected replace failure", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != lastGood {
		t.Fatalf("last-good file changed after failed replace: %q", got)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".pxgo.ini.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary persistence files leaked after failed replace: %v", matches)
	}
}

func TestParseArgsReportsCorruptPlaintextKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(`{"pxgo":`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PXGO_KEYRING_PLAINTEXT", "1")
	t.Setenv("PXGO_KEYRING_FILE", path)
	if _, err := ParseArgs([]string{"--username=test"}); err == nil {
		t.Fatal("ParseArgs silently ignored corrupt plaintext keyring")
	}
}
