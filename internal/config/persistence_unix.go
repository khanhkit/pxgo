//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func replaceFile(from, to string) error {
	return os.Rename(from, to)
}
