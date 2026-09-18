package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
