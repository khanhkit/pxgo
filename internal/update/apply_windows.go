//go:build windows

package update

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const applyHelperPrefix = "--pxgo-apply-update="

type applyHelperPayload struct {
	Target          string   `json:"target"`
	ExpectedVersion string   `json:"expected_version"`
	ParentPID       int      `json:"parent_pid"`
	Restart         bool     `json:"restart,omitempty"`
	RestartArgs     []string `json:"restart_args,omitempty"`
}

type applyHelperResult struct {
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Updated time.Time `json:"updated"`
}

func ApplyCandidate(ctx context.Context, candidate, target, expectedVersion string) (ApplyResult, error) {
	return ApplyCandidateWithRestart(ctx, candidate, target, expectedVersion, nil)
}

func ApplyCandidateWithRestart(ctx context.Context, candidate, target, expectedVersion string, restartArgs []string) (ApplyResult, error) {
	if ctx == nil {
		return ApplyResult{}, errors.New("nil context")
	}
	select {
	case <-ctx.Done():
		return ApplyResult{}, ctx.Err()
	default:
	}
	candidatePath, err := filepath.Abs(candidate)
	if err != nil {
		return ApplyResult{}, err
	}
	targetPath, err := filepath.Abs(target)
	if err != nil {
		return ApplyResult{}, err
	}
	if strings.EqualFold(candidatePath, targetPath) {
		return ApplyResult{}, errors.New("candidate and target executable paths are identical")
	}
	if !strings.EqualFold(filepath.Base(targetPath), "pxgo.exe") {
		return ApplyResult{}, errors.New("direct Windows update target must be pxgo.exe")
	}
	if _, err := normalizeVersion(expectedVersion); err != nil {
		return ApplyResult{}, err
	}
	info, err := os.Stat(candidatePath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("inspect staged candidate: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ApplyResult{}, errors.New("staged candidate is not a regular file")
	}
	payload, err := json.Marshal(applyHelperPayload{
		Target:          targetPath,
		ExpectedVersion: expectedVersion,
		ParentPID:       os.Getpid(),
		Restart:         restartArgs != nil,
		RestartArgs:     append([]string(nil), restartArgs...),
	})
	if err != nil {
		return ApplyResult{}, err
	}
	arg := applyHelperPrefix + base64.RawURLEncoding.EncodeToString(payload)
	// #nosec G204 -- executable is the exact verified staged PxGo candidate and argv is private structured data.
	cmd := exec.Command(candidatePath, arg)
	configureHiddenProcess(cmd)
	cmd.Dir = filepath.Dir(candidatePath)
	if err := cmd.Start(); err != nil {
		return ApplyResult{}, fmt.Errorf("start update replacement helper: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return ApplyResult{}, fmt.Errorf("release update replacement helper: %w", err)
	}
	return ApplyResult{Deferred: true}, nil
}

func RunApplyHelper(args []string) (bool, int) {
	var encoded string
	for _, arg := range args {
		if !strings.HasPrefix(arg, applyHelperPrefix) {
			continue
		}
		if encoded != "" {
			return true, 2
		}
		encoded = strings.TrimPrefix(arg, applyHelperPrefix)
	}
	if encoded == "" {
		return false, 0
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return true, 2
	}
	var payload applyHelperPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return true, 2
	}
	if err := runApplyHelper(payload); err != nil {
		_ = writeApplyHelperResult(false, err)
		return true, 7
	}
	_ = writeApplyHelperResult(true, nil)
	return true, 0
}

func runApplyHelper(payload applyHelperPayload) error {
	if payload.ParentPID <= 0 {
		return errors.New("invalid updater parent pid")
	}
	if _, err := normalizeVersion(payload.ExpectedVersion); err != nil {
		return err
	}
	helperPath, err := os.Executable()
	if err != nil {
		return err
	}
	helperPath, err = filepath.Abs(helperPath)
	if err != nil {
		return err
	}
	targetPath, err := filepath.Abs(payload.Target)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Base(targetPath), "pxgo.exe") {
		return errors.New("update helper target must be pxgo.exe")
	}
	if strings.EqualFold(helperPath, targetPath) {
		return errors.New("update helper must run from staged candidate path")
	}
	if err := waitForParentExit(payload.ParentPID, 2*time.Minute); err != nil {
		return err
	}

	targetInfo, err := os.Lstat(targetPath)
	if err != nil {
		return fmt.Errorf("inspect update target: %w", err)
	}
	if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
		return errors.New("direct update target must be a regular non-symlink file")
	}
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".pxgo-update-new-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	source, err := os.Open(helperPath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(tmp, source)
	closeSourceErr := source.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeSourceErr != nil {
		return closeSourceErr
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	backup := targetPath + ".pxgo-update-backup"
	if _, statErr := os.Lstat(backup); statErr == nil {
		return errors.New("update backup path already exists")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.Rename(targetPath, backup); err != nil {
		return fmt.Errorf("backup current executable: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		rollbackErr := os.Rename(backup, targetPath)
		return errors.Join(fmt.Errorf("activate staged executable: %w", err), rollbackErr)
	}
	removeTemp = false

	verifyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	verifyErr := verifyCandidateVersion(verifyCtx, targetPath, payload.ExpectedVersion)
	cancel()
	if verifyErr != nil {
		removeErr := os.Remove(targetPath)
		rollbackErr := os.Rename(backup, targetPath)
		return errors.Join(fmt.Errorf("installed candidate failed version check: %w", verifyErr), removeErr, rollbackErr)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("remove update backup: %w", err)
	}
	if payload.Restart {
		if err := startUpdatedProcess(targetPath, payload.RestartArgs); err != nil {
			return fmt.Errorf("restart updated PxGo: %w", err)
		}
	}
	return nil
}

func startUpdatedProcess(path string, args []string) error {
	// #nosec G204 -- path is the verified replacement target and argv is inherited from the trusted running PxGo process.
	cmd := exec.Command(path, args...)
	configureHiddenProcess(cmd)
	cmd.Dir = filepath.Dir(path)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func waitForParentExit(pid int, timeout time.Duration) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)) // #nosec G115 -- pid is validated positive and Windows PIDs are uint32.
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return fmt.Errorf("open updater parent process: %w", err)
	}
	defer windows.CloseHandle(handle)
	milliseconds := timeout.Milliseconds()
	if milliseconds <= 0 {
		milliseconds = 1
	}
	if milliseconds > int64(^uint32(0)-1) {
		milliseconds = int64(^uint32(0) - 1)
	}
	result, err := windows.WaitForSingleObject(handle, uint32(milliseconds)) // #nosec G115 -- value is bounded to uint32 above.
	if err != nil {
		return fmt.Errorf("wait for updater parent exit: %w", err)
	}
	if result == uint32(windows.WAIT_TIMEOUT) {
		return errors.New("timed out waiting for updater parent to exit")
	}
	return nil
}

func writeApplyHelperResult(ok bool, applyErr error) error {
	helperPath, err := os.Executable()
	if err != nil {
		return err
	}
	result := applyHelperResult{OK: ok, Updated: time.Now().UTC()}
	if applyErr != nil {
		result.Error = applyErr.Error()
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(filepath.Dir(helperPath), "apply-result.json"), data, 0o600)
}
