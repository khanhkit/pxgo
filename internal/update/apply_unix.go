//go:build !windows

package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func ApplyCandidate(ctx context.Context, candidate, target, expectedVersion string) (ApplyResult, error) {
	return ApplyCandidateWithRestart(ctx, candidate, target, expectedVersion, nil)
}

func ApplyCandidateWithRestart(ctx context.Context, candidate, target, expectedVersion string, _ []string) (ApplyResult, error) {
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
	if candidatePath == targetPath {
		return ApplyResult{}, errors.New("candidate and target executable paths are identical")
	}
	targetInfo, err := os.Lstat(targetPath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("inspect update target: %w", err)
	}
	if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
		return ApplyResult{}, errors.New("direct update target must be a regular non-symlink file")
	}
	candidateInfo, err := os.Stat(candidatePath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("inspect staged candidate: %w", err)
	}
	if !candidateInfo.Mode().IsRegular() {
		return ApplyResult{}, errors.New("staged candidate is not a regular file")
	}

	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".pxgo-update-new-*")
	if err != nil {
		return ApplyResult{}, fmt.Errorf("create update target: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	source, err := os.Open(candidatePath)
	if err != nil {
		return ApplyResult{}, err
	}
	_, copyErr := io.Copy(tmp, source)
	closeSourceErr := source.Close()
	if copyErr != nil {
		return ApplyResult{}, fmt.Errorf("copy staged candidate: %w", copyErr)
	}
	if closeSourceErr != nil {
		return ApplyResult{}, fmt.Errorf("close staged candidate: %w", closeSourceErr)
	}
	if err := tmp.Chmod(targetInfo.Mode().Perm()); err != nil {
		return ApplyResult{}, err
	}
	if err := tmp.Sync(); err != nil {
		return ApplyResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return ApplyResult{}, err
	}

	backup := targetPath + ".pxgo-update-backup"
	if _, statErr := os.Lstat(backup); statErr == nil {
		return ApplyResult{}, errors.New("update backup path already exists")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ApplyResult{}, fmt.Errorf("inspect update backup: %w", statErr)
	}
	if err := os.Rename(targetPath, backup); err != nil {
		return ApplyResult{}, fmt.Errorf("backup current executable: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		rollbackErr := os.Rename(backup, targetPath)
		return ApplyResult{}, errors.Join(fmt.Errorf("activate staged executable: %w", err), rollbackErr)
	}
	removeTemp = false
	_ = syncDir(dir)

	verifyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	verifyErr := verifyCandidateVersion(verifyCtx, targetPath, expectedVersion)
	cancel()
	if verifyErr != nil {
		removeErr := os.Remove(targetPath)
		rollbackErr := os.Rename(backup, targetPath)
		_ = syncDir(dir)
		return ApplyResult{}, errors.Join(fmt.Errorf("installed candidate failed version check: %w", verifyErr), removeErr, rollbackErr)
	}
	if err := os.Remove(backup); err != nil {
		return ApplyResult{}, fmt.Errorf("remove update backup: %w", err)
	}
	_ = syncDir(dir)
	return ApplyResult{}, nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
