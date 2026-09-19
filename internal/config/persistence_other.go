//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package config

import (
	"fmt"
	"os"
	"runtime"
)

func lockFile(*os.File) error {
	return fmt.Errorf("interprocess persistence locking is unsupported on %s", runtime.GOOS)
}

func unlockFile(*os.File) error { return nil }

func replaceFile(from, to string) error { return os.Rename(from, to) }
