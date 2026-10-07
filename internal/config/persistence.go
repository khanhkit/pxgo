package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

func withFileLock(path string, perm fs.FileMode, fn func() error) error {
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, perm)
	if err != nil {
		return fmt.Errorf("open persistence lock %s: %w", lockPath, err)
	}
	defer lock.Close()
	if err := lock.Chmod(perm); err != nil {
		return fmt.Errorf("chmod persistence lock %s: %w", lockPath, err)
	}
	if err := lockFile(lock); err != nil {
		return fmt.Errorf("lock persistence file %s: %w", lockPath, err)
	}
	defer func() { _ = unlockFile(lock) }()
	return fn()
}

func writeFileWithBackup(path string, data []byte, defaultPerm fs.FileMode) error {
	return withFileLock(path, defaultPerm, func() error {
		perm := defaultPerm
		if info, err := os.Stat(path); err == nil { // #nosec G703 -- caller-selected config path.
			perm = info.Mode().Perm()
			current, err := os.ReadFile(path) // #nosec G703 -- lock protects caller-selected config path.
			if err != nil {
				return err
			}
			if _, err := createBackup(path, current, perm); err != nil {
				return fmt.Errorf("backup config: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return atomicWriteFile(path, data, perm)
	})
}

func mutateFileWithBackup(path string, mutate func([]byte) ([]byte, error)) (bool, error) {
	if mutate == nil {
		return false, errors.New("nil persistence mutator")
	}
	changed := false
	err := withFileLock(path, 0o600, func() error {
		info, err := os.Stat(path) // #nosec G703 -- caller-selected config path.
		if err != nil {
			return err
		}
		current, err := os.ReadFile(path) // #nosec G703 -- lock protects caller-selected config path.
		if err != nil {
			return err
		}
		updated, err := mutate(current)
		if err != nil {
			return err
		}
		if bytes.Equal(current, updated) {
			return nil
		}
		perm := info.Mode().Perm()
		backup, err := createBackup(path, current, perm)
		if err != nil {
			return fmt.Errorf("backup config: %w", err)
		}
		if err := atomicWriteFile(path, updated, perm); err != nil {
			return fmt.Errorf("write config after backup %s: %w", backup, err)
		}
		changed = true
		return nil
	})
	return changed, err
}

func createBackup(path string, data []byte, perm fs.FileMode) (string, error) {
	backup := path + ".bak." + time.Now().UTC().Format("20060102T150405.000000000Z")
	f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) // #nosec G304 -- derived from caller-selected config path.
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(backup)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(backup)
		return "", err
	}
	return backup, nil
}

func atomicWriteFile(path string, data []byte, perm fs.FileMode) error {
	return atomicWriteFileWithReplace(path, data, perm, replaceFile)
}

func atomicWriteFileWithReplace(path string, data []byte, perm fs.FileMode, replace func(string, string) error) error {
	if replace == nil {
		return errors.New("nil atomic replace function")
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary persistence file: %w", err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temporary persistence file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary persistence file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary persistence file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary persistence file: %w", err)
	}
	if err := replace(tmpPath, path); err != nil {
		return fmt.Errorf("replace persistence file: %w", err)
	}
	keep = true
	return nil
}
