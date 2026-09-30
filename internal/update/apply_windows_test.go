//go:build windows

package update

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsApplyHelperReplacesExactTargetAndRestarts(t *testing.T) {
	oldWait := waitForParentExitFunc
	oldVerify := verifyInstalledCandidateFunc
	oldStart := startUpdatedProcessFunc
	defer func() {
		waitForParentExitFunc = oldWait
		verifyInstalledCandidateFunc = oldVerify
		startUpdatedProcessFunc = oldStart
	}()
	waitForParentExitFunc = func(int, time.Duration) error { return nil }
	verifyInstalledCandidateFunc = func(context.Context, string, string) error { return nil }
	restarts := 0
	startUpdatedProcessFunc = func(path string, args []string) error {
		restarts++
		if filepath.Base(path) != "pxgo.exe" || len(args) != 1 || args[0] != "--foreground" {
			t.Fatalf("restart path=%q args=%v", path, args)
		}
		return nil
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "pxgo.exe")
	if err := os.WriteFile(target, []byte("old executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantHash := fileSHA256(t, helper)
	payload := applyHelperPayload{
		Target: target, ExpectedVersion: "1.1.0", ParentPID: 123,
		Restart: true, RestartArgs: []string{"--foreground"},
	}
	if err := runApplyHelper(payload); err != nil {
		t.Fatal(err)
	}
	if got := fileSHA256(t, target); got != wantHash {
		t.Fatalf("target hash=%s want helper hash=%s", got, wantHash)
	}
	if restarts != 1 {
		t.Fatalf("restart calls=%d", restarts)
	}
	if _, err := os.Stat(target + ".pxgo-update-backup"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup retained after success: %v", err)
	}
}

func TestWindowsApplyHelperRollbackRestartsOldTargetAndRecordsFailure(t *testing.T) {
	oldWait := waitForParentExitFunc
	oldVerify := verifyInstalledCandidateFunc
	oldStart := startUpdatedProcessFunc
	defer func() {
		waitForParentExitFunc = oldWait
		verifyInstalledCandidateFunc = oldVerify
		startUpdatedProcessFunc = oldStart
	}()
	waitForParentExitFunc = func(int, time.Duration) error { return nil }
	verifyInstalledCandidateFunc = func(context.Context, string, string) error { return errors.New("candidate health failed") }
	restarts := 0
	startUpdatedProcessFunc = func(path string, _ []string) error {
		restarts++
		if filepath.Base(path) != "pxgo.exe" {
			t.Fatalf("restart path=%q", path)
		}
		return nil
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "pxgo.exe")
	oldBytes := []byte("known good executable")
	if err := os.WriteFile(target, oldBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "update-state.json")
	payload := applyHelperPayload{
		Target: target, ExpectedVersion: "1.1.0", ParentPID: 123,
		Restart: true, RestartArgs: []string{"--foreground"},
		StatePath: statePath, CurrentVersion: "1.0.0", Provider: ProviderDirect, Channel: Stable,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	handled, code := RunApplyHelper([]string{applyHelperPrefix + base64.RawURLEncoding.EncodeToString(encoded)})
	if !handled || code != 7 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(oldBytes) {
		t.Fatalf("rollback target=%q want %q", got, oldBytes)
	}
	if restarts != 1 {
		t.Fatalf("rollback restart calls=%d", restarts)
	}
	state, err := ReadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastResult != ResultApplyFailed || state.Current != "1.0.0" || !state.Available {
		t.Fatalf("rollback state=%+v", state)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
