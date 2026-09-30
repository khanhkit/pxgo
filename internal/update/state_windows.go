//go:build windows

package update

import (
	"errors"
	"os"
)

func replaceStateFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}
