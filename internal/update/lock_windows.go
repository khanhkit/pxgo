//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type updateLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireUpdateLock(path string) (*updateLock, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lock := &updateLock{file: file}
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&lock.overlapped,
	)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrUpdateBusy
		}
		return nil, err
	}
	return lock, nil
}

func (l *updateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
