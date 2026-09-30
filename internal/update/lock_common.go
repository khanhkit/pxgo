package update

import (
	"errors"
	"path/filepath"
)

var ErrUpdateBusy = errors.New("another PxGo update is already in progress")

const updateLockFileName = "update.lock"

func updateLockPath(statePath string) string {
	if statePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(statePath), updateLockFileName)
}
